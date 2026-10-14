package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestReactorFailedVideoFetchRouteReturnsProviderError(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	previous := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previous; _ = sqlDB.Close() })
	if err = db.AutoMigrate(&model.Task{}, &model.Channel{}); err != nil {
		t.Fatal(err)
	}
	channel := &model.Channel{Id: 60, Type: constant.ChannelTypeReactor}
	if err = db.Create(channel).Error; err != nil {
		t.Fatal(err)
	}
	task := &model.Task{TaskID: "public_failed_reactor", UserId: 7, ChannelId: 60, Platform: constant.TaskPlatform("60"), Status: model.TaskStatusFailure, FailReason: "Forward execution thread failed: upstream GPU worker unavailable"}
	if err = db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/v1/videos/:task_id", func(c *gin.Context) {
		c.Set("id", 7)
		if taskErr := RelayTaskFetch(c, relayconstant.RelayModeVideoFetchByID); taskErr != nil {
			t.Errorf("fetch error: %+v", taskErr)
		}
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/videos/public_failed_reactor", nil))
	var video dto.OpenAIVideo
	if err = common.Unmarshal(response.Body.Bytes(), &video); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || video.Status != dto.VideoStatusFailed || video.Error == nil || video.Error.Code != "generation_failed" || video.Error.Message != task.FailReason {
		t.Fatalf("failed GET lost provider error: status=%d body=%s", response.Code, response.Body.String())
	}
}
