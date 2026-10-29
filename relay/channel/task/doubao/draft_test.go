package doubao

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func draftFinalInfo() *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		OriginModelName: "sv-seedance-2.5",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "dreamina-seedance-2-5-261231",
			IsModelMapped:     true,
		},
		TaskRelayInfo: &relaycommon.TaskRelayInfo{
			SeedanceDraftOrigin: &relaycommon.SeedanceDraftOrigin{
				UpstreamTaskID:    "cgt-draft",
				UpstreamModelName: "dreamina-seedance-2-5-260628",
				OutputSeconds:     6,
				HasVideoInput:     true,
				InputVideoSeconds: 4,
			},
		},
	}
}

func jsonTaskContext(t *testing.T, body string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c
}

func buildBody(t *testing.T, c *gin.Context, info *relaycommon.RelayInfo) (string, error) {
	t.Helper()
	body, err := (&TaskAdaptor{}).BuildRequestBody(c, info)
	if err != nil {
		return "", err
	}
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), nil
}

func TestSeedanceDraftDefaultsTo480p(t *testing.T) {
	info := seedance25Info()
	info.TaskRelayInfo = &relaycommon.TaskRelayInfo{}
	payload, err := (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
		Model:    "sv-seedance-2.5",
		Prompt:   "draft",
		Mode:     "text-to-video",
		Metadata: map[string]interface{}{"draft": true},
	}, info)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Draft == nil || !bool(*payload.Draft) || payload.Resolution != "480p" {
		t.Fatalf("draft = %v, resolution = %q, want draft 480p", payload.Draft, payload.Resolution)
	}
	if !info.SeedanceDraft {
		t.Fatal("draft request was not marked as a Seedance draft")
	}
}

func TestSeedanceDraftRejections(t *testing.T) {
	for _, tc := range []struct {
		name     string
		upstream string
		metadata map[string]interface{}
		want     string
	}{
		{"720p draft", "dreamina-seedance-2-5-260628", map[string]interface{}{"draft": true, "resolution": "720p"}, "draft only supports 480p"},
		{"2.0 draft", "doubao-seedance-2-0-260128", map[string]interface{}{"draft": true, "resolution": "480p"}, "only supported by seedance 2.5"},
		{"raw draft_task content", "dreamina-seedance-2-5-260628", map[string]interface{}{"content": []interface{}{
			map[string]interface{}{"type": "draft_task", "draft_task": map[string]interface{}{"id": "cgt-someone-else"}},
		}}, "draft_task content is not accepted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: tc.upstream}}
			_, err := (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
				Model: "sv-seedance", Prompt: "draft", Metadata: tc.metadata,
			}, info)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSeedanceDraftBillsAt480p(t *testing.T) {
	c := jsonTaskContext(t, `{"model":"sv-seedance-2.5","prompt":"draft","metadata":{"draft":true,"duration":5}}`)
	info := &relaycommon.RelayInfo{TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
	if taskErr := (&TaskAdaptor{}).ValidateRequestAndSetAction(c, info); taskErr != nil {
		t.Fatal(taskErr.Message)
	}
	if info.BillingRequestInput == nil {
		t.Fatal("draft request without resolution should override billing input")
	}
	if got := gjson.GetBytes(info.BillingRequestInput.Body, "metadata.resolution").String(); got != "480p" {
		t.Fatalf("billing resolution = %q, want 480p", got)
	}
}

func TestSeedanceDraftFinalPayloadMatchesBytePlusStep2(t *testing.T) {
	c := jsonTaskContext(t, `{"model":"sv-seedance-2.5","metadata":{"draft_task_id":"task_draft","resolution":"1080p","output_format":"mov","watermark":true}}`)
	info := draftFinalInfo()
	if taskErr := (&TaskAdaptor{}).ValidateRequestAndSetAction(c, info); taskErr != nil {
		t.Fatal(taskErr.Message)
	}
	got, err := buildBody(t, c, info)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"dreamina-seedance-2-5-260628","content":[{"type":"draft_task","draft_task":{"id":"cgt-draft"}}],"resolution":"1080p","watermark":true,"output_format":"mov"}`
	if got != want {
		t.Fatalf("payload =\n%s\nwant\n%s", got, want)
	}
	if info.UpstreamModelName != "dreamina-seedance-2-5-260628" {
		t.Fatalf("upstream model = %q, want the draft's model", info.UpstreamModelName)
	}
}

func TestSeedanceDraftFinalDefaults(t *testing.T) {
	c := jsonTaskContext(t, `{"model":"sv-seedance-2.5","metadata":{"draft_task_id":"task_draft"}}`)
	info := draftFinalInfo()
	if taskErr := (&TaskAdaptor{}).ValidateRequestAndSetAction(c, info); taskErr != nil {
		t.Fatal(taskErr.Message)
	}
	got, err := buildBody(t, c, info)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"dreamina-seedance-2-5-260628","content":[{"type":"draft_task","draft_task":{"id":"cgt-draft"}}],"resolution":"1080p","watermark":false,"output_format":"mp4"}`
	if got != want {
		t.Fatalf("payload =\n%s\nwant\n%s", got, want)
	}

	billing := info.BillingRequestInput.Body
	if gjson.GetBytes(billing, "resolution").String() != "1080p" ||
		gjson.GetBytes(billing, "duration").Float() != 6 ||
		!gjson.GetBytes(billing, "has_video_input").Bool() ||
		gjson.GetBytes(billing, "input_video_duration").Float() != 4 {
		t.Fatalf("billing input = %s, want the draft's duration and video input at 1080p", billing)
	}
}

func TestSeedanceDraftFinalRejectsReusedParams(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"prompt", `{"model":"sv-seedance-2.5","prompt":"again","metadata":{"draft_task_id":"task_draft"}}`, "prompt"},
		{"images", `{"model":"sv-seedance-2.5","images":["https://example.com/a.png"],"metadata":{"draft_task_id":"task_draft"}}`, "images"},
		{"top-level duration", `{"model":"sv-seedance-2.5","duration":5,"metadata":{"draft_task_id":"task_draft"}}`, "duration"},
		{"metadata duration", `{"model":"sv-seedance-2.5","metadata":{"draft_task_id":"task_draft","duration":5}}`, "metadata.duration"},
		{"metadata generate_audio", `{"model":"sv-seedance-2.5","metadata":{"draft_task_id":"task_draft","generate_audio":true}}`, "metadata.generate_audio"},
		{"metadata content", `{"model":"sv-seedance-2.5","metadata":{"draft_task_id":"task_draft","content":[]}}`, "metadata.content"},
		{"draft true", `{"model":"sv-seedance-2.5","metadata":{"draft_task_id":"task_draft","draft":true}}`, "metadata.draft cannot be true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := jsonTaskContext(t, tc.body)
			taskErr := (&TaskAdaptor{}).ValidateRequestAndSetAction(c, draftFinalInfo())
			if taskErr == nil || taskErr.StatusCode != http.StatusBadRequest || !strings.Contains(taskErr.Message, tc.want) {
				t.Fatalf("taskErr = %+v, want 400 mentioning %q", taskErr, tc.want)
			}
		})
	}
}

func TestSeedanceDraftFinalRejectsNon1080p(t *testing.T) {
	c := jsonTaskContext(t, `{"model":"sv-seedance-2.5","metadata":{"draft_task_id":"task_draft","resolution":"720p"}}`)
	info := draftFinalInfo()
	if taskErr := (&TaskAdaptor{}).ValidateRequestAndSetAction(c, info); taskErr != nil {
		t.Fatal(taskErr.Message)
	}
	if _, err := buildBody(t, c, info); err == nil || !strings.Contains(err.Error(), "only supports 1080p") {
		t.Fatalf("error = %v, want 1080p rejection", err)
	}
}

func TestSeedanceDraftTaskExposesDraftFlag(t *testing.T) {
	task := &model.Task{TaskID: "task_draft", Data: []byte(`{"status":"succeeded","content":{"video_url":"https://example.com/v.mp4"}}`)}
	task.PrivateData.SeedanceDraft = true
	data, err := (&TaskAdaptor{}).ConvertToOpenAIVideo(task)
	if err != nil {
		t.Fatal(err)
	}
	if !gjson.GetBytes(data, "metadata.draft").Bool() {
		t.Fatalf("video = %s, want metadata.draft true", data)
	}

	task.PrivateData.SeedanceDraft = false
	data, err = (&TaskAdaptor{}).ConvertToOpenAIVideo(task)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(data, "metadata.draft").Exists() {
		t.Fatalf("video = %s, want no metadata.draft", data)
	}
}
