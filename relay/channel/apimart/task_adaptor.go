package apimart

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

type TaskAdaptor struct {
	taskcommon.BaseBilling
	apiKey  string
	baseURL string
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	a.apiKey = info.ApiKey
	a.baseURL = strings.TrimRight(info.ChannelBaseUrl, "/")
}

func (a *TaskAdaptor) ValidateRequestAndSetAction(_ *gin.Context, _ *relaycommon.RelayInfo) *dto.TaskError {
	return nil
}

func (a *TaskAdaptor) BuildRequestURL(info *relaycommon.RelayInfo) (string, error) {
	baseURL := strings.TrimRight(info.ChannelBaseUrl, "/")
	if baseURL == "" {
		baseURL = a.baseURL
	}
	if baseURL == "" {
		return "", fmt.Errorf("apimart: base url is required")
	}
	return baseURL + "/v1/images/generations", nil
}

func (a *TaskAdaptor) BuildRequestHeader(_ *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	apiKey := info.ApiKey
	if apiKey == "" {
		apiKey = a.apiKey
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	imageReq, ok := info.Request.(*dto.ImageRequest)
	if !ok || imageReq == nil {
		return nil, fmt.Errorf("apimart: invalid async image request")
	}
	submitReq, err := buildSubmitRequest(*imageReq)
	if err != nil {
		return nil, err
	}
	data, err := common.Marshal(submitReq)
	if err != nil {
		return nil, err
	}
	relaycommon.AppendRequestConversionFromRequest(info, submitReq)
	return bytes.NewReader(data), nil
}

func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	return channel.DoTaskApiRequest(a, c, info, requestBody)
}

func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (taskID string, taskData []byte, taskErr *dto.TaskError) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
	}
	_ = resp.Body.Close()

	taskID, err = parseSubmitTaskID(body)
	if err != nil {
		return "", nil, service.TaskErrorWrapper(err, "invalid_response", http.StatusInternalServerError)
	}

	publicTaskID := info.PublicTaskID
	if publicTaskID == "" {
		publicTaskID = model.GenerateTaskID()
		info.PublicTaskID = publicTaskID
	}

	c.JSON(http.StatusOK, dto.ImageTaskResponse{
		ID:        publicTaskID,
		TaskID:    publicTaskID,
		Object:    "image.task",
		Model:     info.OriginModelName,
		Status:    "queued",
		Progress:  0,
		CreatedAt: time.Now().Unix(),
	})
	return taskID, body, nil
}

func (a *TaskAdaptor) FetchTask(baseURL, key string, body map[string]any, proxy string) (*http.Response, error) {
	taskID, ok := body["task_id"].(string)
	if !ok || strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("invalid task_id")
	}
	pollURL := strings.TrimRight(baseURL, "/") + "/v1/tasks/" + taskID
	req, err := http.NewRequest(http.MethodGet, pollURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("new proxy http client failed: %w", err)
	}
	return client.Do(req)
}

func (a *TaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	var tr TaskResponse
	if err := common.Unmarshal(respBody, &tr); err != nil {
		return nil, errors.Wrapf(err, "apimart: unmarshal task response failed: %s", common.LocalLogPreview(string(respBody)))
	}
	if tr.Code != 200 {
		return relaycommon.FailTaskInfo(fmt.Sprintf("apimart: task response code=%d msg=%s", tr.Code, tr.Message)), nil
	}

	taskInfo := &relaycommon.TaskInfo{ResponseBody: respBody}
	switch tr.Data.Status {
	case "completed":
		taskInfo.Status = model.TaskStatusSuccess
		taskInfo.Progress = taskcommon.ProgressComplete
		imageURL := firstTaskImageURL(tr)
		if imageURL == "" {
			taskInfo.Status = model.TaskStatusFailure
			taskInfo.Reason = "apimart: completed but no image url"
			return taskInfo, nil
		}
		taskInfo.Url = imageURL
	case "failed":
		taskInfo.Status = model.TaskStatusFailure
		taskInfo.Progress = taskcommon.ProgressComplete
		taskInfo.Reason = taskErrorMessage(tr)
	case "pending", "queued":
		taskInfo.Status = model.TaskStatusQueued
		taskInfo.Progress = taskProgress(tr.Data.Progress, taskcommon.ProgressQueued)
	default:
		taskInfo.Status = model.TaskStatusInProgress
		taskInfo.Progress = taskProgress(tr.Data.Progress, taskcommon.ProgressInProgress)
	}
	return taskInfo, nil
}

func (a *TaskAdaptor) GetModelList() []string {
	return ModelList
}

func (a *TaskAdaptor) GetChannelName() string {
	return ChannelName
}

func buildSubmitRequest(request dto.ImageRequest) (*SubmitRequest, error) {
	n := 1
	if request.N != nil {
		n = int(*request.N)
	}
	imageURLs, err := imageURLsFromRequest(request)
	if err != nil {
		return nil, err
	}
	var compression *int
	if len(request.OutputCompression) > 0 {
		if err := common.Unmarshal(request.OutputCompression, &compression); err != nil {
			return nil, fmt.Errorf("apimart: invalid output_compression: %w", err)
		}
	}
	return &SubmitRequest{
		Model:             request.Model,
		Prompt:            request.Prompt,
		N:                 &n,
		Size:              request.Size,
		Resolution:        stringFromExtra(request, "resolution"),
		Quality:           request.Quality,
		OutputFormat:      jsonStringValue(request.OutputFormat),
		OutputCompression: compression,
		Background:        jsonStringValue(request.Background),
		Moderation:        jsonStringValue(request.Moderation),
		ImageURLs:         imageURLs,
		MaskURL:           stringFromExtra(request, "mask_url"),
	}, nil
}

func parseSubmitTaskID(body []byte) (string, error) {
	var submitResp SubmitResponse
	if err := common.Unmarshal(body, &submitResp); err != nil {
		return "", fmt.Errorf("apimart: parse submit response: %w", err)
	}
	if submitResp.Code != 200 || len(submitResp.Data) == 0 {
		return "", fmt.Errorf("apimart: submit failed code=%d msg=%s", submitResp.Code, submitResp.Message)
	}
	taskID := submitResp.Data[0].TaskID
	if strings.TrimSpace(taskID) == "" {
		return "", fmt.Errorf("apimart: empty task_id in submit response")
	}
	return taskID, nil
}

func firstTaskImageURL(tr TaskResponse) string {
	if len(tr.Data.Result.Images) == 0 || len(tr.Data.Result.Images[0].URL) == 0 {
		return ""
	}
	return tr.Data.Result.Images[0].URL[0]
}

func taskErrorMessage(tr TaskResponse) string {
	if len(tr.Data.Error) > 0 {
		var s string
		if err := common.Unmarshal(tr.Data.Error, &s); err == nil && strings.TrimSpace(s) != "" {
			return s
		}
		return string(tr.Data.Error)
	}
	if tr.Message != "" {
		return tr.Message
	}
	return "apimart: task failed"
}

func taskProgress(progress int, fallback string) string {
	if progress <= 0 {
		return fallback
	}
	if progress > 100 {
		progress = 100
	}
	return fmt.Sprintf("%d%%", progress)
}
