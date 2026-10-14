package apimart

import (
	"encoding/json"
	"github.com/QuantumNous/new-api/dto"
)

// Submit: POST {base}/v1/images/generations
type SubmitRequest struct {
	Model             string   `json:"model"`
	Prompt            string   `json:"prompt"`
	N                 *int     `json:"n,omitempty"`
	Size              string   `json:"size,omitempty"`
	Resolution        string   `json:"resolution,omitempty"`
	Quality           string   `json:"quality,omitempty"`
	OutputFormat      string   `json:"output_format,omitempty"`
	OutputCompression *int     `json:"output_compression,omitempty"`
	Background        string   `json:"background,omitempty"`
	Moderation        string   `json:"moderation,omitempty"`
	ImageURLs         []string `json:"image_urls,omitempty"`
	MaskURL           string   `json:"mask_url,omitempty"`
}

type SubmitResponse struct {
	Code int `json:"code"`
	Data []struct {
		Status string `json:"status"`
		TaskID string `json:"task_id"`
	} `json:"data"`
	Message string `json:"message,omitempty"`
}

// Poll: GET {base}/v1/tasks/{task_id}
type TaskResponse struct {
	Code int `json:"code"`
	Data struct {
		Status   string   `json:"status"` // pending | completed | failed
		Progress int      `json:"progress"`
		Cost     *float64 `json:"cost,omitempty"`
		Usage    struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
		Result struct {
			Images []struct {
				URL []string `json:"url"`
			} `json:"images"`
		} `json:"result"`
		Error json.RawMessage `json:"error,omitempty"`
	} `json:"data"`
	Message string `json:"message,omitempty"`
}

func (tr TaskResponse) ImageData() []dto.ImageData {
	var data []dto.ImageData
	for _, image := range tr.Data.Result.Images {
		for _, url := range image.URL {
			if url != "" {
				data = append(data, dto.ImageData{Url: url})
			}
		}
	}
	return data
}
