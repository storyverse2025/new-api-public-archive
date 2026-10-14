package controller

import (
	"github.com/QuantumNous/new-api/model"
	"testing"
)

func TestImageTaskResponseReturnsAllGeneratedImages(t *testing.T) {
	task := &model.Task{
		Status: model.TaskStatusSuccess,
		Data:   []byte(`{"code":200,"data":{"status":"completed","cost":0.0265,"result":{"images":[{"url":["https://cdn.example/a.png"]},{"url":["https://cdn.example/b.png"]}]}}}`),
	}
	response := imageTaskResponse(task)
	if len(response.Data) != 2 {
		t.Fatalf("got %d images, want 2", len(response.Data))
	}
}
