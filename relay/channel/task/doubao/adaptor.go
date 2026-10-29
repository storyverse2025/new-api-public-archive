package doubao

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"github.com/samber/lo"
	"github.com/tidwall/sjson"
)

// ============================
// Request / Response structures
// ============================

type ContentItem struct {
	Type      string        `json:"type,omitempty"`
	Text      string        `json:"text,omitempty"`
	ImageURL  *MediaURL     `json:"image_url,omitempty"`
	VideoURL  *MediaURL     `json:"video_url,omitempty"`
	AudioURL  *MediaURL     `json:"audio_url,omitempty"`
	DraftTask *DraftTaskRef `json:"draft_task,omitempty"`
	Role      string        `json:"role,omitempty"`
}

type MediaURL struct {
	URL string `json:"url,omitempty"`
}

type DraftTaskRef struct {
	ID string `json:"id"`
}

type requestPayload struct {
	Model                 string         `json:"model"`
	Content               []ContentItem  `json:"content,omitempty"`
	CallbackURL           string         `json:"callback_url,omitempty"`
	ReturnLastFrame       *dto.BoolValue `json:"return_last_frame,omitempty"`
	ServiceTier           string         `json:"service_tier,omitempty"`
	ExecutionExpiresAfter *dto.IntValue  `json:"execution_expires_after,omitempty"`
	GenerateAudio         *dto.BoolValue `json:"generate_audio,omitempty"`
	Draft                 *dto.BoolValue `json:"draft,omitempty"`
	Tools                 []struct {
		Type string `json:"type,omitempty"`
	} `json:"tools,omitempty"`
	Resolution   string         `json:"resolution,omitempty"`
	Ratio        string         `json:"ratio,omitempty"`
	Duration     *dto.IntValue  `json:"duration,omitempty"`
	Frames       *dto.IntValue  `json:"frames,omitempty"`
	Seed         *dto.IntValue  `json:"seed,omitempty"`
	CameraFixed  *dto.BoolValue `json:"camera_fixed,omitempty"`
	Watermark    *dto.BoolValue `json:"watermark,omitempty"`
	OutputFormat string         `json:"output_format,omitempty"`
}

type responsePayload struct {
	ID string `json:"id"` // task_id
}

type responseTask struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Status  string `json:"status"`
	Content struct {
		VideoURL string `json:"video_url"`
	} `json:"content"`
	Seed            int    `json:"seed"`
	Resolution      string `json:"resolution"`
	Duration        int    `json:"duration"`
	Ratio           string `json:"ratio"`
	FramesPerSecond int    `json:"framespersecond"`
	ServiceTier     string `json:"service_tier"`
	Tools           []struct {
		Type string `json:"type"`
	} `json:"tools"`
	Usage struct {
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
		ToolUsage        struct {
			WebSearch int `json:"web_search"`
		} `json:"tool_usage"`
	} `json:"usage"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}

// ============================
// Adaptor implementation
// ============================

type TaskAdaptor struct {
	taskcommon.BaseBilling
	ChannelType    int
	apiKey         string
	baseURL        string
	channelSetting dto.ChannelSettings
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	a.ChannelType = info.ChannelType
	a.baseURL = info.ChannelBaseUrl
	a.apiKey = info.ApiKey
	a.channelSetting = info.ChannelSetting
}

// ValidateRequestAndSetAction parses body, validates fields and sets default action.
func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) (taskErr *dto.TaskError) {
	if origin := seedanceDraftOrigin(info); origin != nil {
		req, taskErr := relaycommon.BindTaskRequestWithoutPrompt(c, info, constant.TaskActionGenerate)
		if taskErr != nil {
			return taskErr
		}
		if err := validateSeedanceDraftFinalRequest(&req); err != nil {
			return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
		}
		input, err := seedanceDraftFinalBillingInput(origin)
		if err != nil {
			return service.TaskErrorWrapperLocal(err, "billing_rule_request_error", http.StatusInternalServerError)
		}
		info.BillingRequestInput = input
		return nil
	}

	// Accept only POST /v1/video/generations as "generate" action.
	if taskErr := relaycommon.ValidateBasicTaskRequest(c, info, constant.TaskActionGenerate); taskErr != nil {
		return taskErr
	}
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	input, err := seedanceDraftBillingInput(c, &req)
	if err != nil {
		return service.TaskErrorWrapperLocal(err, "billing_rule_request_error", http.StatusInternalServerError)
	}
	if input != nil {
		info.BillingRequestInput = input
	}
	return nil
}

// seedanceDraftOrigin returns the draft task a final-video request was
// resolved from, or nil for any other request.
func seedanceDraftOrigin(info *relaycommon.RelayInfo) *relaycommon.SeedanceDraftOrigin {
	if info == nil || info.TaskRelayInfo == nil {
		return nil
	}
	return info.SeedanceDraftOrigin
}

// Seedance 2.5 draft → final: these are reused from the draft task and BytePlus
// rejects them even when the values match the draft.
var seedanceDraftReusedMetadataKeys = []string{"content", "duration", "ratio", "seed", "generate_audio", "omni_reference_task_type"}

func validateSeedanceDraftFinalRequest(req *relaycommon.TaskSubmitReq) error {
	reused := make([]string, 0)
	if strings.TrimSpace(req.Prompt) != "" {
		reused = append(reused, "prompt")
	}
	if len(req.Images) > 0 {
		reused = append(reused, "images")
	}
	if len(req.Audios) > 0 {
		reused = append(reused, "audios")
	}
	if len(req.Videos) > 0 {
		reused = append(reused, "videos")
	}
	if req.Duration != 0 || strings.TrimSpace(req.Seconds) != "" {
		reused = append(reused, "duration")
	}
	for _, key := range seedanceDraftReusedMetadataKeys {
		if _, ok := req.Metadata[key]; ok {
			reused = append(reused, "metadata."+key)
		}
	}
	if len(reused) > 0 {
		return fmt.Errorf("%s cannot be set with metadata.draft_task_id: they are reused from the draft task", strings.Join(reused, ", "))
	}

	var params struct {
		Draft *dto.BoolValue `json:"draft,omitempty"`
	}
	if err := taskcommon.UnmarshalMetadata(req.Metadata, &params); err != nil {
		return errors.Wrap(err, "unmarshal metadata failed")
	}
	if params.Draft != nil && bool(*params.Draft) {
		return fmt.Errorf("metadata.draft cannot be true with metadata.draft_task_id")
	}
	return nil
}

// seedanceDraftFinalBillingInput bills the final video like the draft it came
// from: same output duration and input video, at 1080p.
func seedanceDraftFinalBillingInput(origin *relaycommon.SeedanceDraftOrigin) (*billingexpr.RequestInput, error) {
	body := map[string]any{"resolution": seedanceDraftFinalResolution}
	if origin.OutputSeconds > 0 {
		body["duration"] = origin.OutputSeconds
	}
	if origin.HasVideoInput {
		body["has_video_input"] = true
		body["input_video_duration"] = origin.InputVideoSeconds
	}
	data, err := common.Marshal(body)
	if err != nil {
		return nil, err
	}
	return &billingexpr.RequestInput{Body: data}, nil
}

// seedanceDraftBillingInput bills a draft request without an explicit
// resolution at 480p, the only resolution a draft can have. Returns nil when
// the request body needs no change.
func seedanceDraftBillingInput(c *gin.Context, req *relaycommon.TaskSubmitReq) (*billingexpr.RequestInput, error) {
	var params struct {
		Draft      *dto.BoolValue `json:"draft,omitempty"`
		Resolution string         `json:"resolution,omitempty"`
	}
	if err := taskcommon.UnmarshalMetadata(req.Metadata, &params); err != nil {
		return nil, errors.Wrap(err, "unmarshal metadata failed")
	}
	if params.Draft == nil || !bool(*params.Draft) || strings.TrimSpace(params.Resolution) != "" {
		return nil, nil
	}
	if !strings.HasPrefix(c.GetHeader("Content-Type"), "application/json") {
		return nil, nil
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, err
	}
	body, err := storage.Bytes()
	if err != nil {
		return nil, err
	}
	body, err = sjson.SetBytes(body, "metadata.resolution", seedanceDraftResolution)
	if err != nil {
		return nil, err
	}
	return &billingexpr.RequestInput{Body: body}, nil
}

// BuildRequestURL constructs the upstream URL.
func (a *TaskAdaptor) BuildRequestURL(_ *relaycommon.RelayInfo) (string, error) {
	return seedanceTaskEndpoint(a.baseURL, a.channelSeedanceEndpoint()), nil
}

// BuildRequestHeader sets required headers.
func (a *TaskAdaptor) BuildRequestHeader(_ *gin.Context, req *http.Request, _ *relaycommon.RelayInfo) error {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	return nil
}

// EstimateBilling 检测请求 metadata 中是否包含视频输入，返回视频折扣 OtherRatio。
func (a *TaskAdaptor) EstimateBilling(c *gin.Context, info *relaycommon.RelayInfo) map[string]float64 {
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return nil
	}
	if req.HasVideo() || hasVideoInMetadata(req.Metadata) {
		if ratio, ok := GetVideoInputRatio(info.OriginModelName); ok {
			return map[string]float64{"video_input": ratio}
		}
	}
	return nil
}

// hasVideoInMetadata 直接检查 metadata 的 content 数组是否包含 video_url 条目，
// 避免构建完整的上游 requestPayload。
func hasVideoInMetadata(metadata map[string]interface{}) bool {
	if metadata == nil {
		return false
	}
	contentRaw, ok := metadata["content"]
	if !ok {
		return false
	}
	contentSlice, ok := contentRaw.([]interface{})
	if !ok {
		return false
	}
	for _, item := range contentSlice {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if itemMap["type"] == "video_url" {
			return true
		}
		if _, has := itemMap["video_url"]; has {
			return true
		}
	}
	return false
}

// BuildRequestBody converts request into Doubao specific format.
func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return nil, err
	}

	var body *requestPayload
	origin := seedanceDraftOrigin(info)
	if origin != nil {
		body, err = convertDraftFinalPayload(&req, origin, info.UpstreamModelName)
	} else {
		body, err = a.convertToRequestPayload(&req, info)
	}
	if err != nil {
		return nil, errors.Wrap(err, "convert request payload failed")
	}
	// A final video must use the draft task's upstream model, even if the
	// channel's model mapping changed since.
	if info.IsModelMapped && origin == nil {
		body.Model = info.UpstreamModelName
	} else {
		info.UpstreamModelName = body.Model
	}
	data, err := common.Marshal(body)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}

// DoRequest delegates to common helper.
func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	return channel.DoTaskApiRequest(a, c, info, requestBody)
}

// DoResponse handles upstream response, returns taskID etc.
func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (taskID string, taskData []byte, taskErr *dto.TaskError) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		taskErr = service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
		return
	}
	_ = resp.Body.Close()

	// Parse Doubao response
	var dResp responsePayload
	if err := common.Unmarshal(responseBody, &dResp); err != nil {
		taskErr = service.TaskErrorWrapper(errors.Wrapf(err, "body: %s", responseBody), "unmarshal_response_body_failed", http.StatusInternalServerError)
		return
	}

	if dResp.ID == "" {
		taskErr = service.TaskErrorWrapper(fmt.Errorf("task_id is empty"), "invalid_response", http.StatusInternalServerError)
		return
	}

	ov := dto.NewOpenAIVideo()
	ov.ID = info.PublicTaskID
	ov.TaskID = info.PublicTaskID
	ov.CreatedAt = time.Now().Unix()
	ov.Model = info.OriginModelName

	c.JSON(http.StatusOK, ov)
	return dResp.ID, responseBody, nil
}

// FetchTask fetch task status
func (a *TaskAdaptor) FetchTask(baseUrl, key string, body map[string]any, proxy string) (*http.Response, error) {
	taskID, ok := body["task_id"].(string)
	if !ok {
		return nil, fmt.Errorf("invalid task_id")
	}

	overrideEndpoint, _ := body["seedance_endpoint"].(string)
	uri := fmt.Sprintf("%s/%s", seedanceTaskEndpoint(baseUrl, overrideEndpoint), taskID)

	req, err := http.NewRequest(http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("new proxy http client failed: %w", err)
	}
	return client.Do(req)
}

func (a *TaskAdaptor) GetModelList() []string {
	return ModelList
}

func (a *TaskAdaptor) GetChannelName() string {
	return ChannelName
}

// Seedance content reference limits (Ark `content[]`). Mirrors the upstream caps
// used by the native bragi-canvas Seedance provider.
const (
	seedanceTaskPath = "/api/v3/contents/generations/tasks"

	seedance20MaxReferenceImages = 9
	seedance20MaxReferenceAudios = 3
	seedance20MaxReferenceVideos = 3

	seedance25MaxReferenceImages = 30
	seedance25MaxReferenceAudios = 10
	seedance25MaxReferenceVideos = 10
	seedance25MaxReferences      = 50

	// Draft mode (2.5 only): a draft is always 480p and the final video
	// generated from it is always 1080p.
	seedanceDraftResolution      = "480p"
	seedanceDraftFinalResolution = "1080p"
)

func (a *TaskAdaptor) channelSeedanceEndpoint() string {
	if a == nil {
		return ""
	}
	return a.channelSetting.BytePlusSeedanceEndpoint
}

func seedanceTaskEndpoint(baseURL, overrideEndpoint string) string {
	endpoint := strings.TrimRight(strings.TrimSpace(overrideEndpoint), "/")
	if endpoint == "" {
		base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
		if strings.HasSuffix(base, seedanceTaskPath) {
			return base
		}
		return base + seedanceTaskPath
	}
	return endpoint
}

func isSeedance25Model(model string) bool {
	model = strings.ToLower(model)
	return strings.Contains(model, "seedance-2-5") || strings.Contains(model, "seedance-2.5")
}

func effectiveSeedanceModel(req *relaycommon.TaskSubmitReq, info *relaycommon.RelayInfo) string {
	if info != nil {
		// Endpoint IDs identify deployments, not model capabilities. Keep them
		// for BuildRequestBody's upstream model, but validate against the
		// original model when the mapping hides the Seedance version.
		upstream := strings.TrimSpace(info.UpstreamModelName)
		if upstream != "" && !strings.HasPrefix(strings.ToLower(upstream), "ep-") {
			return upstream
		}
		if info.OriginModelName != "" {
			return info.OriginModelName
		}
	}
	return req.Model
}

func taskMode(req *relaycommon.TaskSubmitReq) string {
	if mode := strings.TrimSpace(req.Mode); mode != "" {
		return mode
	}
	for _, key := range []string{"genMode", "gen_mode", "mode"} {
		if v, ok := req.Metadata[key].(string); ok {
			if mode := strings.TrimSpace(v); mode != "" {
				return mode
			}
		}
	}
	return ""
}

func seedanceReferenceRole(mediaType, mode string, index int) string {
	if mediaType != "image" {
		return "reference_" + mediaType
	}
	switch mode {
	case "first-frame":
		return "first_frame"
	case "first-last-frame":
		if index == 0 {
			return "first_frame"
		}
		return "last_frame"
	default:
		return "reference_image"
	}
}

func contentReferenceCounts(content []ContentItem) (images, audios, videos int) {
	for _, item := range content {
		switch item.Type {
		case "image_url":
			images++
		case "audio_url":
			audios++
		case "video_url":
			videos++
		}
	}
	return
}

func seedanceContentHasRole(content []ContentItem, role string) bool {
	for _, item := range content {
		if item.Role == role {
			return true
		}
	}
	return false
}

func validateSeedancePayload(model, mode string, p *requestPayload) error {
	imageCount, audioCount, videoCount := contentReferenceCounts(p.Content)
	is25 := isSeedance25Model(model)
	for _, item := range p.Content {
		if item.Type == "draft_task" || item.DraftTask != nil {
			return fmt.Errorf("draft_task content is not accepted; reference a draft with metadata.draft_task_id")
		}
	}
	if p.Draft != nil && bool(*p.Draft) {
		if !is25 {
			return fmt.Errorf("draft mode is only supported by seedance 2.5")
		}
		if strings.ToLower(strings.TrimSpace(p.Resolution)) != seedanceDraftResolution {
			return fmt.Errorf("seedance 2.5 draft only supports %s output", seedanceDraftResolution)
		}
	}
	maxImages, maxAudios, maxVideos := seedance20MaxReferenceImages, seedance20MaxReferenceAudios, seedance20MaxReferenceVideos
	modelName := "seedance"
	if is25 {
		maxImages, maxAudios, maxVideos = seedance25MaxReferenceImages, seedance25MaxReferenceAudios, seedance25MaxReferenceVideos
		modelName = "seedance 2.5"
	}
	if imageCount > maxImages {
		return fmt.Errorf("%s supports up to %d reference images", modelName, maxImages)
	}
	if audioCount > maxAudios {
		return fmt.Errorf("%s supports up to %d reference audios", modelName, maxAudios)
	}
	if videoCount > maxVideos {
		return fmt.Errorf("%s supports up to %d reference videos", modelName, maxVideos)
	}
	if is25 && imageCount+audioCount+videoCount > seedance25MaxReferences {
		return fmt.Errorf("seedance 2.5 supports up to %d total references", seedance25MaxReferences)
	}
	if mode != "" {
		totalRefs := imageCount + audioCount + videoCount
		supportedModes := map[string]bool{
			"text-to-video": true,
			"first-frame":   true,
			"image-ref":     true,
			"video-ref":     true,
		}
		if is25 {
			supportedModes["first-last-frame"] = true
			supportedModes["video-extend"] = true
			supportedModes["video-edit"] = true
		}
		if !supportedModes[mode] {
			return fmt.Errorf("%s does not support %s mode", modelName, mode)
		}
		switch mode {
		case "text-to-video":
			if totalRefs > 0 {
				return fmt.Errorf("%s text-to-video mode does not accept reference media", modelName)
			}
		case "first-frame":
			if imageCount != 1 || audioCount > 0 || videoCount > 0 || !seedanceContentHasRole(p.Content, "first_frame") {
				return fmt.Errorf("%s first-frame mode requires exactly one first-frame image and no other reference media", modelName)
			}
		case "first-last-frame":
			if imageCount != 2 || audioCount > 0 || videoCount > 0 || !seedanceContentHasRole(p.Content, "first_frame") || !seedanceContentHasRole(p.Content, "last_frame") {
				return fmt.Errorf("%s first-last-frame mode requires first and last frame images and no other reference media", modelName)
			}
		case "image-ref":
			if imageCount == 0 {
				return fmt.Errorf("%s image-ref mode requires at least one reference image", modelName)
			}
		case "video-ref":
			if is25 && totalRefs == 0 {
				return fmt.Errorf("%s video-ref mode requires reference media", modelName)
			}
			if !is25 && imageCount+videoCount == 0 {
				return fmt.Errorf("%s video-ref mode requires image or video reference media", modelName)
			}
		case "video-extend", "video-edit":
			if videoCount == 0 {
				return fmt.Errorf("%s %s mode requires at least one reference video", modelName, mode)
			}
		}
	}

	if is25 {
		duration := 0
		if p.Duration != nil {
			duration = int(*p.Duration)
		}
		if duration != -1 && (duration < 4 || duration > 30) {
			return fmt.Errorf("seedance 2.5 duration must be Auto (-1) or an integer from 4 to 30 seconds")
		}
		if mode == "video-edit" && duration != -1 {
			return fmt.Errorf("seedance 2.5 video-edit mode requires Auto (-1) duration")
		}
		ratio := p.Ratio
		if ratio == "" {
			ratio = "adaptive"
		}
		if !lo.Contains([]string{"adaptive", "16:9", "4:3", "1:1", "3:4", "9:16", "21:9"}, ratio) {
			return fmt.Errorf("seedance 2.5 does not support ratio %s", ratio)
		}
		if lo.Contains([]string{"first-frame", "first-last-frame", "video-extend", "video-edit"}, mode) && ratio != "adaptive" {
			return fmt.Errorf("seedance 2.5 %s mode requires adaptive ratio", mode)
		}
		resolution := strings.ToLower(strings.TrimSpace(p.Resolution))
		if resolution == "" {
			resolution = "720p"
		}
		if !lo.Contains([]string{"480p", "720p", "1080p"}, resolution) {
			return fmt.Errorf("seedance 2.5 does not support %s output", p.Resolution)
		}
		outputFormat := strings.ToLower(strings.TrimSpace(p.OutputFormat))
		if outputFormat == "" {
			outputFormat = "mp4"
		}
		if !lo.Contains([]string{"mp4", "mov"}, outputFormat) {
			return fmt.Errorf("seedance 2.5 does not support %s output format", p.OutputFormat)
		}
	}

	return nil
}

func (a *TaskAdaptor) convertToRequestPayload(req *relaycommon.TaskSubmitReq, info *relaycommon.RelayInfo) (*requestPayload, error) {
	modelName := effectiveSeedanceModel(req, info)
	mode := taskMode(req)
	r := requestPayload{
		Model:   req.Model,
		Content: []ContentItem{},
	}

	// Build reference content with explicit roles, matching the Ark content API:
	// image_url/reference_image, audio_url/reference_audio, video_url/reference_video.
	for _, imgURL := range req.Images {
		r.Content = append(r.Content, ContentItem{
			Type:     "image_url",
			ImageURL: &MediaURL{URL: imgURL},
			Role:     seedanceReferenceRole("image", mode, len(r.Content)),
		})
	}
	for _, audioURL := range req.Audios {
		r.Content = append(r.Content, ContentItem{
			Type:     "audio_url",
			AudioURL: &MediaURL{URL: audioURL},
			Role:     seedanceReferenceRole("audio", mode, 0),
		})
	}
	for _, videoURL := range req.Videos {
		r.Content = append(r.Content, ContentItem{
			Type:     "video_url",
			VideoURL: &MediaURL{URL: videoURL},
			Role:     seedanceReferenceRole("video", mode, 0),
		})
	}

	// metadata may still supply a full `content` array (overrides the above) plus
	// scalar params (resolution, ratio, …). Only struct-declared keys survive.
	metadata := req.Metadata
	if err := taskcommon.UnmarshalMetadata(metadata, &r); err != nil {
		return nil, errors.Wrap(err, "unmarshal metadata failed")
	}

	if r.Draft != nil && bool(*r.Draft) {
		r.Resolution = taskcommon.DefaultString(r.Resolution, seedanceDraftResolution)
		if info != nil && info.TaskRelayInfo != nil {
			info.SeedanceDraft = true
		}
	}
	if isSeedance25Model(modelName) {
		r.Ratio = taskcommon.DefaultString(r.Ratio, "adaptive")
		r.Resolution = taskcommon.DefaultString(r.Resolution, "720p")
		if r.Duration == nil {
			r.Duration = lo.ToPtr(dto.IntValue(-1))
		}
		if r.GenerateAudio == nil {
			r.GenerateAudio = lo.ToPtr(dto.BoolValue(true))
		}
		if r.Watermark == nil {
			r.Watermark = lo.ToPtr(dto.BoolValue(false))
		}
		if r.OutputFormat == "" {
			r.OutputFormat = "mp4"
		}
	}

	if req.Duration != 0 {
		r.Duration = lo.ToPtr(dto.IntValue(req.Duration))
	}
	if sec, err := strconv.Atoi(req.Seconds); err == nil && sec != 0 {
		r.Duration = lo.ToPtr(dto.IntValue(sec))
	}

	r.Content = lo.Reject(r.Content, func(c ContentItem, _ int) bool { return c.Type == "text" })
	r.Content = append(r.Content, ContentItem{
		Type: "text",
		Text: req.Prompt,
	})

	if err := validateSeedancePayload(modelName, mode, &r); err != nil {
		return nil, err
	}

	return &r, nil
}

// seedanceDraftFinalParams are the parameters a draft → final request may set.
// BytePlus does not carry them over from the draft: omitted ones fall back to
// defaults, so the gateway's 2.5 defaults apply.
type seedanceDraftFinalParams struct {
	Resolution            string         `json:"resolution,omitempty"`
	ReturnLastFrame       *dto.BoolValue `json:"return_last_frame,omitempty"`
	OutputFormat          string         `json:"output_format,omitempty"`
	Watermark             *dto.BoolValue `json:"watermark,omitempty"`
	ServiceTier           string         `json:"service_tier,omitempty"`
	ExecutionExpiresAfter *dto.IntValue  `json:"execution_expires_after,omitempty"`
	CallbackURL           string         `json:"callback_url,omitempty"`
}

// convertDraftFinalPayload builds the BytePlus Step 2 request: the draft task
// reference plus the re-specifiable parameters, nothing reused from the draft.
func convertDraftFinalPayload(req *relaycommon.TaskSubmitReq, origin *relaycommon.SeedanceDraftOrigin, fallbackModel string) (*requestPayload, error) {
	var params seedanceDraftFinalParams
	if err := taskcommon.UnmarshalMetadata(req.Metadata, &params); err != nil {
		return nil, errors.Wrap(err, "unmarshal metadata failed")
	}

	resolution := strings.ToLower(strings.TrimSpace(params.Resolution))
	if resolution == "" {
		resolution = seedanceDraftFinalResolution
	}
	if resolution != seedanceDraftFinalResolution {
		return nil, fmt.Errorf("seedance 2.5 final video from a draft only supports %s output", seedanceDraftFinalResolution)
	}
	outputFormat := strings.ToLower(strings.TrimSpace(params.OutputFormat))
	if outputFormat == "" {
		outputFormat = "mp4"
	}
	if !lo.Contains([]string{"mp4", "mov"}, outputFormat) {
		return nil, fmt.Errorf("seedance 2.5 does not support %s output format", params.OutputFormat)
	}
	watermark := params.Watermark
	if watermark == nil {
		watermark = lo.ToPtr(dto.BoolValue(false))
	}

	model := origin.UpstreamModelName
	if model == "" {
		model = fallbackModel
	}
	return &requestPayload{
		Model:                 model,
		Content:               []ContentItem{{Type: "draft_task", DraftTask: &DraftTaskRef{ID: origin.UpstreamTaskID}}},
		Resolution:            resolution,
		ReturnLastFrame:       params.ReturnLastFrame,
		OutputFormat:          outputFormat,
		Watermark:             watermark,
		ServiceTier:           params.ServiceTier,
		ExecutionExpiresAfter: params.ExecutionExpiresAfter,
		CallbackURL:           params.CallbackURL,
	}, nil
}

func (a *TaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	resTask := responseTask{}
	if err := common.Unmarshal(respBody, &resTask); err != nil {
		return nil, errors.Wrap(err, "unmarshal task result failed")
	}

	taskResult := relaycommon.TaskInfo{
		Code: 0,
	}

	// Map Doubao status to internal status
	switch resTask.Status {
	case "pending", "queued":
		taskResult.Status = model.TaskStatusQueued
		taskResult.Progress = "10%"
	case "processing", "running":
		taskResult.Status = model.TaskStatusInProgress
		taskResult.Progress = "50%"
	case "succeeded":
		taskResult.Status = model.TaskStatusSuccess
		taskResult.Progress = "100%"
		taskResult.Url = resTask.Content.VideoURL
		// 解析 usage 信息用于按倍率计费
		taskResult.CompletionTokens = resTask.Usage.CompletionTokens
		taskResult.TotalTokens = resTask.Usage.TotalTokens
	case "failed":
		taskResult.Status = model.TaskStatusFailure
		taskResult.Progress = "100%"
		taskResult.Reason = resTask.Error.Message
	default:
		// Unknown status, treat as processing
		taskResult.Status = model.TaskStatusInProgress
		taskResult.Progress = "30%"
	}

	return &taskResult, nil
}

func (a *TaskAdaptor) ConvertToOpenAIVideo(originTask *model.Task) ([]byte, error) {
	var dResp responseTask
	if err := common.Unmarshal(originTask.Data, &dResp); err != nil {
		return nil, errors.Wrap(err, "unmarshal doubao task data failed")
	}

	openAIVideo := dto.NewOpenAIVideo()
	openAIVideo.ID = originTask.TaskID
	openAIVideo.TaskID = originTask.TaskID
	openAIVideo.Status = originTask.Status.ToVideoStatus()
	openAIVideo.SetProgressStr(originTask.Progress)
	openAIVideo.SetMetadata("url", dResp.Content.VideoURL)
	openAIVideo.CreatedAt = originTask.CreatedAt
	openAIVideo.CompletedAt = originTask.FinishTime
	openAIVideo.Model = originTask.Properties.OriginModelName
	if originTask.PrivateData.SeedanceDraft {
		openAIVideo.SetMetadata("draft", true)
	}

	if dResp.Status == "failed" {
		openAIVideo.Error = &dto.OpenAIVideoError{
			Message: dResp.Error.Message,
			Code:    dResp.Error.Code,
		}
	}

	return common.Marshal(openAIVideo)
}
