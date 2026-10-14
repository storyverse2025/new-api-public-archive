package controller

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestVideoProxyReactorAuthenticatesContentAndKeepsPrivateCache(t *testing.T) {
	service.InitHttpClient()
	upstreamRequests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests++
		if r.URL.Path != "/tasks/reactor_abc/content" || r.Header.Get("Authorization") != "Bearer stored-worker-secret" {
			t.Errorf("invalid worker request: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Cache-Control", "private, no-store")
		_, _ = io.WriteString(w, "authenticated-video-bytes")
	}))
	defer upstream.Close()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	previousDB := model.DB
	previousCache := common.MemoryCacheEnabled
	model.DB = db
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { model.DB = previousDB; common.MemoryCacheEnabled = previousCache; _ = sqlDB.Close() })
	if err = db.AutoMigrate(&model.Channel{}, &model.Task{}); err != nil {
		t.Fatal(err)
	}
	base := upstream.URL
	ch := &model.Channel{Id: 600, Type: constant.ChannelTypeReactor, Key: "new-worker-secret", BaseURL: &base}
	if err = db.Create(ch).Error; err != nil {
		t.Fatal(err)
	}
	task := &model.Task{TaskID: "public_reactor_task", UserId: 7, ChannelId: 600, Status: model.TaskStatusSuccess, PrivateData: model.TaskPrivateData{UpstreamTaskID: "reactor_abc", Key: "stored-worker-secret"}}
	if err = db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/public_reactor_task/content", nil)
	c.Set("id", 7)
	c.Params = gin.Params{{Key: "task_id", Value: "public_reactor_task"}}
	VideoProxy(c)
	if response.Code != http.StatusOK || response.Body.String() != "authenticated-video-bytes" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if actual := response.Header().Get("Cache-Control"); actual != "private, no-store" {
		t.Fatalf("private media cache policy changed to %q", actual)
	}
	denied := httptest.NewRecorder()
	c, _ = gin.CreateTestContext(denied)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/public_reactor_task/content", nil)
	c.Set("id", 8)
	c.Params = gin.Params{{Key: "task_id", Value: "public_reactor_task"}}
	VideoProxy(c)
	if denied.Code != http.StatusNotFound || upstreamRequests != 1 {
		t.Fatalf("different user fetched private media: status=%d upstream=%d", denied.Code, upstreamRequests)
	}
}
