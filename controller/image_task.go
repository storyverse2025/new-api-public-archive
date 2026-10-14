package controller

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	"github.com/QuantumNous/new-api/relay/channel/apimart"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

func RelayImageTask(c *gin.Context) {
	c.Set("relay_mode", relayconstant.RelayModeImagesGenerations)
	imageReq, err := helper.GetAndValidOpenAIImageRequest(c, relayconstant.RelayModeImagesGenerations)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatOpenAIImage, imageReq, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	relayInfo.TaskRelayInfo = &relaycommon.TaskRelayInfo{}
	relayInfo.Action = constant.TaskActionGenerate
	relayInfo.PublicTaskID = model.GenerateTaskID()
	relayInfo.ForcePreConsume = true

	retryParam := &service.RetryParam{
		Ctx:        c,
		TokenGroup: relayInfo.TokenGroup,
		ModelName:  relayInfo.OriginModelName,
		Retry:      common.GetPointer(0),
	}
	channelModel, channelErr := getChannel(c, relayInfo, retryParam)
	if channelErr != nil {
		c.JSON(channelErr.StatusCode, gin.H{"error": channelErr.ToOpenAIError()})
		return
	}
	addUsedChannel(c, channelModel.Id)
	relayInfo.InitChannelMeta(c)
	if relayInfo.ChannelType != constant.ChannelTypeAPImart {
		c.JSON(http.StatusBadRequest, gin.H{"error": "async image tasks are only supported for apimart channels"})
		return
	}

	if err := helper.ModelMappedHelper(c, relayInfo, imageReq); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	meta := imageReq.GetTokenCountMeta()
	tokens, err := service.EstimateRequestToken(c, meta, relayInfo)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	relayInfo.SetEstimatePromptTokens(tokens)
	priceData, err := helper.ModelPriceHelper(c, relayInfo, tokens, meta)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	imageN := imageTaskN(imageReq)
	if relayInfo.BillingCalcSnapshot == nil && priceData.UsePrice && imageN > 1 {
		priceData.AddOtherRatio("n", float64(imageN))
	}
	quota := priceData.QuotaToPreConsume
	// Provider rules already include n in their reservation.
	if relayInfo.BillingCalcSnapshot == nil {
		quota = imageTaskQuota(quota, imageN)
	}
	priceData.Quota = quota
	priceData.QuotaToPreConsume = quota
	relayInfo.PriceData = priceData

	if !priceData.FreeModel {
		if apiErr := service.PreConsumeBilling(c, quota, relayInfo); apiErr != nil {
			c.JSON(apiErr.StatusCode, gin.H{"error": apiErr.ToOpenAIError()})
			return
		}
	}
	var taskErr *dto.TaskError
	defer func() {
		if taskErr != nil && relayInfo.Billing != nil {
			relayInfo.Billing.Refund(c)
		}
	}()

	adaptor := relay.GetTaskAdaptor(constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeAPImart)))
	if adaptor == nil {
		taskErr = service.TaskErrorWrapperLocal(fmt.Errorf("apimart task adaptor not found"), "invalid_channel", http.StatusInternalServerError)
		respondTaskError(c, taskErr)
		return
	}
	adaptor.Init(relayInfo)

	requestBody, err := adaptor.BuildRequestBody(c, relayInfo)
	if err != nil {
		taskErr = service.TaskErrorWrapper(err, "build_request_failed", http.StatusInternalServerError)
		respondTaskError(c, taskErr)
		return
	}
	resp, err := adaptor.DoRequest(c, relayInfo, requestBody)
	if err != nil {
		taskErr = service.TaskErrorWrapper(err, "do_request_failed", http.StatusInternalServerError)
		respondTaskError(c, taskErr)
		return
	}
	if resp != nil && resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		taskErr = service.TaskErrorWrapper(fmt.Errorf("%s", string(responseBody)), "fail_to_submit_image_task", resp.StatusCode)
		respondTaskError(c, taskErr)
		return
	}

	upstreamTaskID, taskData, taskErr := adaptor.DoResponse(c, resp, relayInfo)
	if taskErr != nil {
		respondTaskError(c, taskErr)
		return
	}

	if settleErr := service.SettleBilling(c, relayInfo, quota); settleErr != nil {
		common.SysError("settle image task billing error: " + settleErr.Error())
	}
	consumeLogId := service.LogTaskConsumption(c, relayInfo)
	task := model.InitTask(constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeAPImart)), relayInfo)
	task.PrivateData.UpstreamTaskID = upstreamTaskID
	task.PrivateData.BillingSource = relayInfo.BillingSource
	task.PrivateData.SubscriptionId = relayInfo.SubscriptionId
	task.PrivateData.TokenId = relayInfo.TokenId
	task.PrivateData.ConsumeLogId = consumeLogId
	task.PrivateData.BillingContext = &model.TaskBillingContext{
		ModelPrice:              relayInfo.PriceData.ModelPrice,
		GroupRatio:              relayInfo.PriceData.GroupRatioInfo.GroupRatio,
		ModelRatio:              relayInfo.PriceData.ModelRatio,
		OtherRatios:             relayInfo.PriceData.OtherRatios,
		OriginModelName:         relayInfo.OriginModelName,
		PerCallBilling:          relayInfo.BillingCalcSnapshot == nil,
		ProviderBillingSnapshot: relayInfo.BillingCalcSnapshot,
	}
	task.Quota = quota
	task.Data = taskData
	task.Action = relayInfo.Action
	if insertErr := task.Insert(); insertErr != nil {
		logger.LogError(c, fmt.Sprintf("insert image task error: %s", insertErr.Error()))
	}
}

func RelayImageTaskFetch(c *gin.Context) {
	taskID := c.Param("task_id")
	userID := c.GetInt("id")
	task, exist, err := model.GetByTaskId(userID, taskID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !exist {
		c.JSON(http.StatusBadRequest, gin.H{"error": "task_not_exist"})
		return
	}
	c.JSON(http.StatusOK, imageTaskResponse(task))
}

func imageTaskN(req *dto.ImageRequest) int {
	if req == nil || req.N == nil || *req.N == 0 {
		return 1
	}
	return int(*req.N)
}

func imageTaskQuota(baseQuota, n int) int {
	if baseQuota <= 0 {
		return 0
	}
	if n <= 1 {
		return baseQuota
	}
	return baseQuota * n
}

func imageTaskResponse(task *model.Task) dto.ImageTaskResponse {
	resp := dto.ImageTaskResponse{
		ID:        task.TaskID,
		TaskID:    task.TaskID,
		Object:    "image.task",
		Model:     task.Properties.OriginModelName,
		Status:    imageTaskStatus(task.Status),
		Progress:  imageTaskProgress(task.Progress),
		CreatedAt: task.CreatedAt,
	}
	if task.Status == model.TaskStatusSuccess {
		resp.CompletedAt = task.FinishTime
		if resp.CompletedAt == 0 {
			resp.CompletedAt = task.UpdatedAt
		}
		var upstream apimart.TaskResponse
		if common.Unmarshal(task.Data, &upstream) == nil {
			resp.Data = upstream.ImageData()
		}
		if url := task.GetResultURL(); len(resp.Data) == 0 && url != "" {
			resp.Data = []dto.ImageData{{Url: url}}
		}
	}
	if task.Status == model.TaskStatusFailure {
		resp.CompletedAt = task.FinishTime
		resp.Error = task.FailReason
	}
	return resp
}

func imageTaskStatus(status model.TaskStatus) string {
	switch status {
	case model.TaskStatusQueued, model.TaskStatusSubmitted, model.TaskStatusNotStart:
		return "queued"
	case model.TaskStatusInProgress:
		return "in_progress"
	case model.TaskStatusSuccess:
		return "completed"
	case model.TaskStatusFailure:
		return "failed"
	default:
		return "unknown"
	}
}

func imageTaskProgress(progress string) int {
	progress = strings.TrimSpace(strings.TrimSuffix(progress, "%"))
	if progress == "" {
		return 0
	}
	value, err := strconv.Atoi(progress)
	if err != nil {
		return 0
	}
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}
