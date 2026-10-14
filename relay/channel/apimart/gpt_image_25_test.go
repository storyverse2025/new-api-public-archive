package apimart

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/billingcalc"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func TestGPTImage25RequestParameters(t *testing.T) {
	for _, name := range []string{"gpt-image-2.5-flare", "gpt-image-2.5-sunburst"} {
		t.Run(name, func(t *testing.T) {
			var req dto.ImageRequest
			body := []byte(`{"model":"` + name + `","prompt":"edit the product","n":2,"quality":"xhigh","resolution":"4k","size":"16:9","background":"transparent","moderation":"auto","output_format":"webp","output_compression":0,"image_urls":["https://cdn.example/ref.png"],"mask_url":"https://cdn.example/mask.png"}`)
			if err := common.Unmarshal(body, &req); err != nil {
				t.Fatal(err)
			}
			converted, err := (&Adaptor{}).ConvertImageRequest(nil, nil, req)
			if err != nil {
				t.Fatal(err)
			}
			data, err := common.Marshal(converted)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := common.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]any{"model": name, "n": float64(2), "quality": "xhigh", "resolution": "4k", "size": "16:9", "background": "transparent", "moderation": "auto", "output_format": "webp", "output_compression": float64(0), "mask_url": "https://cdn.example/mask.png"} {
				if got[key] != want {
					t.Errorf("%s = %v, want %v", key, got[key], want)
				}
			}
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			reader, err := (&TaskAdaptor{}).BuildRequestBody(ctx, &relaycommon.RelayInfo{Request: &req})
			if err != nil {
				t.Fatal(err)
			}
			async, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, async) {
				t.Fatalf("sync/async conversion differs: %s vs %s", data, async)
			}
		})
	}
}

func TestGPTImage25SyncReturnsAllImagesAndActualCost(t *testing.T) {
	service.InitHttpClient()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing upstream key")
		}
		io.WriteString(w, `{"code":200,"data":{"status":"completed","cost":0.0265,"usage":{"input_tokens":32,"output_tokens":878,"total_tokens":910},"result":{"images":[{"url":["https://cdn.example/a.png"]},{"url":["https://cdn.example/b.png"]}]}}}`)
	}))
	defer srv.Close()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	info := &relaycommon.RelayInfo{}
	estimate, err := billingcalc.Estimate("upstream_cost", billingcalc.Context{RequestBody: []byte(`{"n":2}`), RuleParams: map[string]any{"reserve_price_per_image": 1.0}, GroupRatio: 1, QuotaPerUnit: 500000})
	if err != nil {
		t.Fatal(err)
	}
	info.BillingCalcSnapshot = estimate.Snapshot
	usage, apiErr := (&Adaptor{baseURL: srv.URL, apiKey: "test-key"}).DoResponse(ctx, &http.Response{Body: io.NopCloser(strings.NewReader(`{"code":200,"data":[{"task_id":"task_test"}]}`))}, info)
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	var response dto.ImageResponse
	if err := common.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 2 {
		t.Fatalf("got %d images, want 2", len(response.Data))
	}
	if usage.(*dto.Usage).TotalTokens != 910 {
		t.Fatalf("lost actual usage: %+v", usage)
	}
}

func TestGPTImage25OptionalParametersOmitted(t *testing.T) {
	converted, err := buildSubmitRequest(dto.ImageRequest{Model: "gpt-image-2.5-flare", Prompt: "a cube"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := common.Marshal(converted)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := common.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"background", "moderation", "output_compression"} {
		if _, exists := got[key]; exists {
			t.Errorf("%s should be omitted", key)
		}
	}
}

func TestGPTImage25TaskPreservesBillingResponse(t *testing.T) {
	body := []byte(`{"code":200,"data":{"status":"completed","cost":0.01325,"result":{"images":[{"url":["https://cdn.example/out.png"]}]}}}`)
	result, err := (&TaskAdaptor{}).ParseTaskResult(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result.ResponseBody, body) {
		t.Fatal("poll result lost actual upstream cost")
	}
}

func TestGPTImage25ModelsRegistered(t *testing.T) {
	for _, name := range []string{"gpt-image-2.5-flare", "gpt-image-2.5-sunburst"} {
		found := false
		for _, model := range ModelList {
			if model == name {
				found = true
			}
		}
		if !found {
			t.Errorf("model %s not registered", name)
		}
	}
}
