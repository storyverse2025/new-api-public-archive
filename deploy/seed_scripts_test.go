package deploy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
)

func TestSeedScriptsRegisterModelsAndBillingWithRootToken(t *testing.T) {
	for _, name := range []string{"bash", "curl", "jq"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s required: %v", name, err)
		}
	}
	channels := map[string]map[string]any{}
	options := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if req.Header.Get("Authorization") != "Bearer test-root-token" || req.Header.Get("New-Api-User") != "1" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"success":false,"message":"missing root identity"}`)
			return
		}
		if req.Method == http.MethodGet {
			io.WriteString(w, `{"success":true,"data":{"items":[]}}`)
			return
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var value map[string]any
		if err := common.Unmarshal(body, &value); err != nil {
			t.Errorf("invalid shell JSON: %s: %v", body, err)
			return
		}
		switch req.URL.Path {
		case "/api/option/":
			options[value["key"].(string)] = value["value"].(string)
		case "/api/channel/":
			channel := value["channel"].(map[string]any)
			channels[channel["name"].(string)] = channel
		}
		io.WriteString(w, `{"success":true}`)
	}))
	defer server.Close()
	env := append(os.Environ(),
		"GATEWAY_URL="+server.URL, "GATEWAY_ROOT_ACCESS_TOKEN=test-root-token",
		"TOKENROUTER_API_KEY=test", "APIMART_API_KEY=test-apimart", "BYTEPLUS_ARK_API_KEY=test",
		"BYTEPLUS_ACCESS_KEY=test", "BYTEPLUS_SECRET_KEY=test", "BYTEPLUS_GROUP_ID=test",
		"SEEDREAM_LITE_ENDPOINT_ID=test", "SEEDANCE_20_ENDPOINT_ID=test", "FAL_API_KEY=test",
		"REACTOR_WORKER_SECRET=test-worker-secret", "REACTOR_SESSION_MAX_SECONDS=60",
		"APIMART_GPT_IMAGE25_RESERVE_PER_IMAGE=0.5", "APIMART_GPT_IMAGE25_COST_MULTIPLIER=1.2",
		"BILLING_RULES_FILE=",
	)
	// Run copies in a temporary directory so real .env.prod credentials cannot load.
	dir := t.TempDir()
	for _, script := range []string{"seed_channels.sh", "seed_billing_rules.sh"} {
		data, err := os.ReadFile(script)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, script)
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", file)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s failed: %v\n%s", script, err, out)
		}
	}
	apimart := channels["apimart"]
	if apimart == nil {
		t.Fatal("missing APIMart channel")
	}
	var mapping map[string]string
	if err := common.Unmarshal([]byte(apimart["model_mapping"].(string)), &mapping); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"flare", "sunburst"} {
		model := "gpt-image-2.5-" + suffix
		if mapping["sv-"+model] != model || !strings.Contains(apimart["models"].(string), "sv-"+model) {
			t.Fatalf("missing %s mapping", model)
		}
	}
	reactor := channels["reactor"]
	if reactor == nil || reactor["key"] != "test-worker-secret" || reactor["type"] != float64(60) {
		t.Fatalf("invalid Reactor channel: %+v", reactor)
	}
	if err := common.Unmarshal([]byte(reactor["model_mapping"].(string)), &mapping); err != nil {
		t.Fatal(err)
	}
	if mapping["sv-fast-h3-omni"] != "reactor/fast-h3-omni" {
		t.Fatal("missing Reactor mapping")
	}
	var rules map[string]string
	var params map[string]map[string]any
	if err := common.Unmarshal([]byte(options["billing_setting.billing_rule"]), &rules); err != nil {
		t.Fatal(err)
	}
	if err := common.Unmarshal([]byte(options["billing_setting.billing_rule_params"]), &params); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"sv-gpt-image-2.5-flare", "sv-gpt-image-2.5-sunburst", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst"} {
		key := "channel_name:apimart:" + model
		if rules[key] != "upstream_cost" || params[key]["reserve_price_per_image"] != 0.5 || params[key]["cost_multiplier"] != 1.2 {
			t.Fatalf("invalid image billing: %s %+v", key, params[key])
		}
	}
	key := "channel_name:reactor:sv-fast-h3-omni"
	if rules[key] != "reactor_gpu_seconds" || params[key]["reserve_session_seconds"] != float64(60) || params[key]["price_per_second"] != 0.0125 {
		t.Fatalf("invalid Reactor billing: %+v", params[key])
	}
}
