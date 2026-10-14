package reactor

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func TestBuildRequestPreservesFractionalDurationAndZeroSeed(t *testing.T) {
	req := relaycommon.TaskSubmitReq{Prompt: "The subject in <Picture 1> speaks", Size: "9:16", Images: []string{"https://example.com/ref.png"}, Audios: []string{"https://example.com/voice.mp3"}, Metadata: map[string]any{"seconds": 5.875, "seed": 0, "first_frame_image": "https://example.com/start.png"}}
	payload, err := buildRequest(req, "reactor/fast-h3-omni")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := common.Marshal(payload)
	var actual map[string]any
	_ = common.Unmarshal(data, &actual)
	if actual["seconds"] != 5.875 || actual["seed"] != float64(0) || actual["canvas"] != "9:16" {
		t.Fatalf("bad payload %s", data)
	}
	if len(payload.ReferenceImages) != 1 || len(payload.ReferenceAudios) != 1 || payload.FirstFrameImage == "" {
		t.Fatalf("references lost: %s", data)
	}
}

func TestBuildRequestRejectsUnsupportedParameters(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  relaycommon.TaskSubmitReq
	}{
		{"duration", relaycommon.TaskSubmitReq{Prompt: "scene", Duration: 6}},
		{"audio_without_image", relaycommon.TaskSubmitReq{Prompt: "scene", Audios: []string{"https://example.com/audio.mp3"}}},
		{"keyframe_without_reference", relaycommon.TaskSubmitReq{Prompt: "scene", Metadata: map[string]any{"first_frame_image": "https://example.com/a.png"}}},
		{"unknown_parameter", relaycommon.TaskSubmitReq{Prompt: "scene", Metadata: map[string]any{"resolution": "720p"}}},
		{"video_input", relaycommon.TaskSubmitReq{Prompt: "scene", Videos: []string{"https://example.com/video.mp4"}}},
		{"unsafe_media", relaycommon.TaskSubmitReq{Prompt: "scene", Images: []string{"file:///etc/passwd"}}},
		{"cross_session_continuation", relaycommon.TaskSubmitReq{Prompt: "scene", Metadata: map[string]any{"continue_from_clip_id": "other"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildRequest(tc.req, "reactor/fast-h3-omni"); err == nil {
				t.Fatal("expected invalid request")
			}
		})
	}
	if _, err := buildRequest(relaycommon.TaskSubmitReq{Prompt: "scene"}, "unrelated/model"); err == nil {
		t.Fatal("expected invalid model")
	}
}

func TestWorkerPollingUsesStoredCredentialsAndReturnsProxyContentURL(t *testing.T) {
	service.InitHttpClient()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tasks/task_abc" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected request: path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"task_id":"task_abc","status":"completed","output_ready":true,"session_seconds":17.2,"duration":5.875}`)
	}))
	defer server.Close()
	a := &TaskAdaptor{}
	resp, err := a.FetchTask(server.URL, "test-key", map[string]any{"task_id": "task_abc"}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	result, err := a.ParseTaskResult(data)
	if err != nil || result.Status != model.TaskStatusSuccess || result.Url != "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestCompletedWithoutOutputFailsAndErrorsRemainVisible(t *testing.T) {
	a := &TaskAdaptor{}
	for _, body := range []string{`{"status":"completed","output_ready":false}`, `{"status":"failed","error":"model refused request"}`} {
		result, err := a.ParseTaskResult([]byte(body))
		if err != nil || result.Status != model.TaskStatusFailure || result.Reason == "" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
}

func TestBuildWorkerRequestRefusesDirectReactorAPI(t *testing.T) {
	a := &TaskAdaptor{}
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
	info.ChannelBaseUrl = "https://api.reactor.inc"
	info.ApiKey = "test-key"
	a.Init(info)
	if _, err := a.BuildRequestURL(&relaycommon.RelayInfo{}); err == nil {
		t.Fatal("must not pretend Reactor upstream is async REST")
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("task_request", relaycommon.TaskSubmitReq{Prompt: "scene", Seconds: "5.875"})
	info.ChannelBaseUrl = "http://worker:8091"
	a.Init(info)
	info.UpstreamModelName = "reactor/fast-h3-omni"
	body, err := a.BuildRequestBody(c, info)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(body)
	if !strings.Contains(string(b), `"seconds":5.875`) {
		t.Fatalf("lost exact seconds: %s", b)
	}
}

func TestAcceptedSubmissionRemainsSuccessfulAtGateway(t *testing.T) {
	service.InitHttpClient()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tasks" || r.Header.Get("Authorization") != "Bearer private-worker-key" {
			t.Fatal("invalid private worker request")
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"task_id":"reactor_abc","status":"queued"}`)
	}))
	defer server.Close()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{}`))
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: server.URL, ApiKey: "private-worker-key"}}
	a := &TaskAdaptor{}
	a.Init(info)
	resp, err := a.DoRequest(c, info, strings.NewReader(`{"model":"reactor/fast-h3-omni"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("gateway rejects accepted task with status=%d", resp.StatusCode)
	}
}

func TestContentURLUsesTrustedWorkerAndRejectsTaskPathInjection(t *testing.T) {
	actual, err := ContentURL("http://reactor-worker:8091", "reactor_abc")
	if err != nil || actual != "http://reactor-worker:8091/tasks/reactor_abc/content" {
		t.Fatalf("url=%q err=%v", actual, err)
	}
	for _, id := range []string{"../other", "id?redirect=evil", "id/content"} {
		if _, err := ContentURL("http://reactor-worker:8091", id); err == nil {
			t.Fatal("path injection accepted")
		}
	}
}

func TestCanonicalVideoRequestValidatesBeforeUpstreamModelMapping(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"model":"sv-fast-h3-omni","prompt":"The subject in <Picture 1> speaks","metadata":{"seconds":5.875,"canvas":"1:1","reference_images":["https://example.com/reference.png"],"reference_audios":["https://example.com/voice.wav"],"seed":0}}`))
	c.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "sv-fast-h3-omni"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
	a := &TaskAdaptor{}
	if taskErr := a.ValidateRequestAndSetAction(c, info); taskErr != nil {
		t.Fatalf("canonical model rejected before mapping: %s", taskErr.Message)
	}
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil || req.Model != "sv-fast-h3-omni" {
		t.Fatalf("stored request=%+v err=%v", req, err)
	}
	info.UpstreamModelName = "reactor/fast-h3-omni"
	body, err := a.BuildRequestBody(c, info)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(body)
	var payload WorkerRequest
	if err = common.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Model != "reactor/fast-h3-omni" || payload.Seconds != 5.875 || payload.Seed == nil || *payload.Seed != 0 || len(payload.ReferenceImages) != 1 || len(payload.ReferenceAudios) != 1 {
		t.Fatalf("mapped payload lost fields: %s", data)
	}
	info.UpstreamModelName = "unrelated/model"
	if _, err = a.BuildRequestBody(c, info); err == nil {
		t.Fatal("unsupported mapped model accepted")
	}
}

func TestCanonicalRequestStillRejectsInvalidPayloadBeforeMapping(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"model":"sv-fast-h3-omni","prompt":"scene","metadata":{"seconds":6}}`))
	c.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "sv-fast-h3-omni"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
	if err := (&TaskAdaptor{}).ValidateRequestAndSetAction(c, info); err == nil || !strings.Contains(err.Message, "seconds must be") {
		t.Fatalf("payload validation error=%+v", err)
	}
}

func TestFailedVideoConversionPreservesProviderReason(t *testing.T) {
	task := &model.Task{TaskID: "public_failure", Status: model.TaskStatusFailure, FailReason: "Forward execution thread failed: GPU worker unavailable"}
	data, err := (&TaskAdaptor{}).ConvertToOpenAIVideo(task)
	if err != nil {
		t.Fatal(err)
	}
	var video dto.OpenAIVideo
	if err = common.Unmarshal(data, &video); err != nil {
		t.Fatal(err)
	}
	if video.Status != dto.VideoStatusFailed || video.Error == nil || video.Error.Code != "generation_failed" || video.Error.Message != task.FailReason {
		t.Fatalf("failed response lost provider reason: %s", data)
	}
}

func TestSuccessfulVideoConversionDoesNotExposeStaleFailure(t *testing.T) {
	task := &model.Task{TaskID: "public_success", Status: model.TaskStatusSuccess, FailReason: "old reason", PrivateData: model.TaskPrivateData{ResultURL: "https://gateway.example/v1/videos/public_success/content"}}
	data, err := (&TaskAdaptor{}).ConvertToOpenAIVideo(task)
	if err != nil {
		t.Fatal(err)
	}
	var video dto.OpenAIVideo
	if err = common.Unmarshal(data, &video); err != nil {
		t.Fatal(err)
	}
	if video.Status != dto.VideoStatusCompleted || video.Error != nil {
		t.Fatalf("unexpected error for success: %s", data)
	}
}
