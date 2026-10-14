package reactor

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const ModelName = "reactor/fast-h3-omni"

var ModelList = []string{ModelName}

type WorkerRequest struct {
	Model           string   `json:"model"`
	Prompt          string   `json:"prompt"`
	Seconds         float64  `json:"seconds"`
	Canvas          string   `json:"canvas"`
	ReferenceImages []string `json:"reference_images,omitempty"`
	ReferenceAudios []string `json:"reference_audios,omitempty"`
	FirstFrameImage string   `json:"first_frame_image,omitempty"`
	LastFrameImage  string   `json:"last_frame_image,omitempty"`
	Seed            *int     `json:"seed,omitempty"`
}

type WorkerResponse struct {
	TaskID      string `json:"task_id"`
	Status      string `json:"status"`
	OutputReady bool   `json:"output_ready"`
	Error       string `json:"error,omitempty"`
	Data        struct {
		DurationSeconds       float64 `json:"duration_seconds"`
		OutputDurationSeconds float64 `json:"output_duration_seconds"`
	} `json:"data"`
}

type TaskAdaptor struct {
	taskcommon.BaseBilling
	baseURL string
	key     string
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	a.baseURL = strings.TrimRight(info.ChannelBaseUrl, "/")
	a.key = info.ApiKey
}
func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	if err := relaycommon.ValidateBasicTaskRequest(c, info, constant.TaskActionGenerate); err != nil {
		return err
	}
	req, err := relaycommon.GetTaskRequest(c)
	if err == nil {
		// ModelMappedHelper runs after validation. Validate the payload shape
		// here; BuildRequestBody checks the actual mapped upstream model later.
		_, err = buildRequest(req, ModelName)
	}
	if err != nil {
		return service.TaskErrorWrapper(err, "invalid_request", http.StatusBadRequest)
	}
	return nil
}
func workerBaseURL(base string) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u == nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("reactor: configure an internal SDK worker base URL")
	}
	if strings.EqualFold(u.Hostname(), "api.reactor.inc") || strings.EqualFold(u.Hostname(), "reactor.inc") || strings.EqualFold(u.Hostname(), "www.reactor.inc") {
		return "", fmt.Errorf("reactor: the realtime upstream requires the internal SDK worker; it is not an async REST API")
	}
	return strings.TrimRight(base, "/"), nil
}
func (a *TaskAdaptor) BuildRequestURL(*relaycommon.RelayInfo) (string, error) {
	base, err := workerBaseURL(a.baseURL)
	if err != nil {
		return "", err
	}
	return base + "/tasks", nil
}
func (a *TaskAdaptor) BuildRequestHeader(_ *gin.Context, req *http.Request, _ *relaycommon.RelayInfo) error {
	req.Header.Set("Authorization", "Bearer "+a.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return nil
}
func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return nil, err
	}
	payload, err := buildRequest(req, info.UpstreamModelName)
	if err != nil {
		return nil, err
	}
	data, err := common.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}
func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, body io.Reader) (*http.Response, error) {
	resp, err := channel.DoTaskApiRequest(a, c, info, body)
	if err == nil && resp != nil && resp.StatusCode == http.StatusAccepted {
		resp.StatusCode = http.StatusOK
		resp.Status = "200 OK"
	}
	return resp, err
}
func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (string, []byte, *dto.TaskError) {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusBadGateway)
	}
	var result WorkerResponse
	if err = common.Unmarshal(body, &result); err != nil || result.TaskID == "" {
		return "", nil, service.TaskErrorWrapper(fmt.Errorf("reactor: invalid worker submission"), "invalid_response", http.StatusBadGateway)
	}
	ov := dto.NewOpenAIVideo()
	ov.ID = info.PublicTaskID
	ov.TaskID = info.PublicTaskID
	ov.Model = info.OriginModelName
	ov.CreatedAt = time.Now().Unix()
	c.JSON(http.StatusOK, ov)
	return result.TaskID, body, nil
}
func (a *TaskAdaptor) FetchTask(baseURL, key string, body map[string]any, proxy string) (*http.Response, error) {
	id, _ := body["task_id"].(string)
	if !validTaskID(id) {
		return nil, fmt.Errorf("reactor: invalid task_id")
	}
	base, err := workerBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, base+"/tasks/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}
func (a *TaskAdaptor) ParseTaskResult(body []byte) (*relaycommon.TaskInfo, error) {
	var result WorkerResponse
	if err := common.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("reactor: invalid worker status: %w", err)
	}
	task := &relaycommon.TaskInfo{ResponseBody: body}
	switch result.Status {
	case "completed":
		task.Progress = taskcommon.ProgressComplete
		if result.OutputReady {
			task.Status = model.TaskStatusSuccess
		} else {
			task.Status = model.TaskStatusFailure
			task.Reason = "reactor: completed without exported video"
		}
	case "failed":
		task.Status = model.TaskStatusFailure
		task.Progress = taskcommon.ProgressComplete
		task.Reason = result.Error
		if task.Reason == "" {
			task.Reason = "reactor: generation failed"
		}
	case "queued":
		task.Status = model.TaskStatusQueued
		task.Progress = taskcommon.ProgressQueued
	case "in_progress":
		task.Status = model.TaskStatusInProgress
		task.Progress = taskcommon.ProgressInProgress
	default:
		return nil, fmt.Errorf("reactor: unknown worker status %q", result.Status)
	}
	return task, nil
}
func (a *TaskAdaptor) GetModelList() []string { return ModelList }
func (a *TaskAdaptor) GetChannelName() string { return "Reactor" }
func (a *TaskAdaptor) ConvertToOpenAIVideo(task *model.Task) ([]byte, error) {
	return common.Marshal(task.ToOpenAIVideo())
}
func validTaskID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func buildRequest(req relaycommon.TaskSubmitReq, upstream string) (*WorkerRequest, error) {
	if upstream != ModelName && upstream != "fast-h3-omni" {
		return nil, fmt.Errorf("reactor: unsupported model %q", upstream)
	}
	if strings.TrimSpace(req.Prompt) == "" || len(req.Prompt) > 16000 {
		return nil, fmt.Errorf("reactor: prompt is required and must be at most 16000 bytes")
	}
	payload := &WorkerRequest{Model: ModelName, Prompt: req.Prompt, Seconds: 14.375, Canvas: "16:9", ReferenceImages: req.Images, ReferenceAudios: req.Audios}
	if req.Size != "" {
		payload.Canvas = req.Size
	}
	if len(payload.ReferenceImages) == 0 && req.Image != "" {
		payload.ReferenceImages = []string{req.Image}
	}
	if req.InputReference != "" && len(payload.ReferenceImages) == 0 {
		payload.ReferenceImages = []string{req.InputReference}
	}
	if req.Duration != 0 {
		payload.Seconds = float64(req.Duration)
	}
	if req.Seconds != "" {
		seconds, err := strconv.ParseFloat(req.Seconds, 64)
		if err != nil {
			return nil, fmt.Errorf("reactor: invalid seconds")
		}
		payload.Seconds = seconds
	}
	for key, value := range req.Metadata {
		switch key {
		case "seconds":
			data, err := common.Marshal(value)
			if err != nil {
				return nil, err
			}
			if err = common.Unmarshal(data, &payload.Seconds); err != nil {
				return nil, fmt.Errorf("reactor: seconds must be a number")
			}
		case "canvas", "aspect_ratio":
			v, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("reactor: canvas must be a string")
			}
			payload.Canvas = v
		case "first_frame_image", "last_frame_image":
			v, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("reactor: keyframes must be image URLs")
			}
			if key == "first_frame_image" {
				payload.FirstFrameImage = v
			} else {
				payload.LastFrameImage = v
			}
		case "reference_images", "reference_audios":
			data, err := common.Marshal(value)
			if err != nil {
				return nil, err
			}
			var refs []string
			if err = common.Unmarshal(data, &refs); err != nil {
				return nil, fmt.Errorf("reactor: references must be URL arrays")
			}
			if key == "reference_images" {
				payload.ReferenceImages = refs
			} else {
				payload.ReferenceAudios = refs
			}
		case "seed":
			data, err := common.Marshal(value)
			if err != nil {
				return nil, err
			}
			if err = common.Unmarshal(data, &payload.Seed); err != nil {
				return nil, fmt.Errorf("reactor: seed must be an integer")
			}
		default:
			return nil, fmt.Errorf("reactor: unsupported parameter %q", key)
		}
	}
	validSeconds := false
	for _, v := range []float64{5.875, 8, 10.125, 12.25, 14.375} {
		if payload.Seconds == v {
			validSeconds = true
		}
	}
	if !validSeconds {
		return nil, fmt.Errorf("reactor: seconds must be 5.875, 8, 10.125, 12.25 or 14.375")
	}
	switch payload.Canvas {
	case "16:9", "1:1", "9:16", "4:3":
	default:
		return nil, fmt.Errorf("reactor: unsupported canvas")
	}
	if len(req.Videos) > 0 || req.Mode != "" {
		return nil, fmt.Errorf("reactor: video input and continuation modes are unsupported for independent jobs")
	}
	if len(payload.ReferenceImages) > 9 || len(payload.ReferenceAudios) > 3 {
		return nil, fmt.Errorf("reactor: at most 9 images and 3 audio references")
	}
	if len(payload.ReferenceImages) == 0 && (len(payload.ReferenceAudios) > 0 || payload.FirstFrameImage != "" || payload.LastFrameImage != "") {
		return nil, fmt.Errorf("reactor: audio and keyframes require a reference image")
	}
	media := append(append([]string{}, payload.ReferenceImages...), payload.ReferenceAudios...)
	media = append(media, payload.FirstFrameImage, payload.LastFrameImage)
	for _, raw := range media {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return nil, fmt.Errorf("reactor: references must be public HTTPS URLs")
		}
	}
	return payload, nil
}

var _ channel.TaskAdaptor = (*TaskAdaptor)(nil)

// ContentURL derives content from the administrator-configured private worker.
// It never follows an upstream result URL supplied by a generated task.
func ContentURL(baseURL, taskID string) (string, error) {
	base, err := workerBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	if !validTaskID(taskID) {
		return "", fmt.Errorf("reactor: invalid task_id")
	}
	return base + "/tasks/" + url.PathEscape(taskID) + "/content", nil
}
