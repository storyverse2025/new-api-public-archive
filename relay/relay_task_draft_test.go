package relay

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingcalc"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestMain(m *testing.M) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		panic("failed to open test db: " + err.Error())
	}
	sqlDB, err := db.DB()
	if err != nil {
		panic("failed to get sql.DB: " + err.Error())
	}
	sqlDB.SetMaxOpenConns(1)
	model.DB = db
	model.LOG_DB = db
	common.UsingSQLite = true
	common.RedisEnabled = false
	if err := db.AutoMigrate(&model.Task{}, &model.Channel{}); err != nil {
		panic("failed to migrate: " + err.Error())
	}
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

const (
	draftOwner      = 1
	draftChannelID  = 7
	draftModel      = "sv-seedance-2.5"
	draftUpstream   = "dreamina-seedance-2-5-260628"
	draftUpstreamID = "cgt-draft"
)

func seedDraftTask(t *testing.T, taskID string, mutate func(*model.Task)) {
	t.Helper()
	task := &model.Task{
		TaskID:    taskID,
		UserId:    draftOwner,
		ChannelId: draftChannelID,
		Status:    model.TaskStatusSuccess,
		Data:      []byte(`{"status":"succeeded","duration":6}`),
		Properties: model.Properties{
			OriginModelName:   draftModel,
			UpstreamModelName: draftUpstream,
		},
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: draftUpstreamID,
			SeedanceDraft:  true,
			BillingContext: &model.TaskBillingContext{
				ProviderBillingSnapshot: &billingcalc.Snapshot{
					RuleName: "byteplus_seedance2",
					Params: map[string]any{
						"output_seconds":      5.0,
						"has_video_input":     true,
						"input_video_seconds": 4.0,
					},
				},
			},
		},
	}
	if mutate != nil {
		mutate(task)
	}
	if err := model.DB.Create(task).Error; err != nil {
		t.Fatal(err)
	}
}

func setupDraftDB(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM tasks")
		model.DB.Exec("DELETE FROM channels")
	})
	channel := &model.Channel{Id: draftChannelID, Name: "byteplus", Key: "sk-test", Status: common.ChannelStatusEnabled}
	if err := model.DB.Create(channel).Error; err != nil {
		t.Fatal(err)
	}
}

func resolveDraft(t *testing.T, userID int, body string) (*relaycommon.RelayInfo, string) {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{
		UserId:          userID,
		OriginModelName: draftModel,
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: draftChannelID},
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{},
	}
	if taskErr := ResolveOriginTask(c, info); taskErr != nil {
		return info, taskErr.Code
	}
	return info, ""
}

func TestResolveSeedanceDraftOrigin(t *testing.T) {
	setupDraftDB(t)
	seedDraftTask(t, "task_draft", nil)

	info, code := resolveDraft(t, draftOwner, `{"model":"sv-seedance-2.5","metadata":{"draft_task_id":"task_draft"}}`)
	if code != "" {
		t.Fatalf("code = %q, want success", code)
	}
	origin := info.SeedanceDraftOrigin
	if origin == nil {
		t.Fatal("draft origin was not resolved")
	}
	if origin.UpstreamTaskID != draftUpstreamID || origin.UpstreamModelName != draftUpstream {
		t.Fatalf("origin = %+v, want upstream task %s on %s", origin, draftUpstreamID, draftUpstream)
	}
	// The upstream's actual duration wins over the pre-consume estimate.
	if origin.OutputSeconds != 6 || !origin.HasVideoInput || origin.InputVideoSeconds != 4 {
		t.Fatalf("origin billing = %+v, want 6s output with 4s input video", origin)
	}
	ch, ok := info.LockedChannel.(*model.Channel)
	if !ok || ch.Id != draftChannelID {
		t.Fatalf("locked channel = %v, want channel %d", info.LockedChannel, draftChannelID)
	}
}

func TestResolveSeedanceDraftOriginRejections(t *testing.T) {
	setupDraftDB(t)
	seedDraftTask(t, "task_draft", nil)
	seedDraftTask(t, "task_running", func(task *model.Task) { task.Status = model.TaskStatusInProgress })
	seedDraftTask(t, "task_normal", func(task *model.Task) { task.PrivateData.SeedanceDraft = false })
	seedDraftTask(t, "task_other_model", func(task *model.Task) { task.Properties.OriginModelName = "sv-seedance-2.0" })

	for _, tc := range []struct {
		name   string
		userID int
		body   string
		want   string
	}{
		{"another user's draft", draftOwner + 1, `{"metadata":{"draft_task_id":"task_draft"}}`, "task_not_exist"},
		{"unknown draft", draftOwner, `{"metadata":{"draft_task_id":"task_missing"}}`, "task_not_exist"},
		{"draft still running", draftOwner, `{"metadata":{"draft_task_id":"task_running"}}`, "draft_task_not_ready"},
		{"not a draft", draftOwner, `{"metadata":{"draft_task_id":"task_normal"}}`, "draft_task_invalid"},
		{"model mismatch", draftOwner, `{"metadata":{"draft_task_id":"task_other_model"}}`, "draft_task_model_mismatch"},
		{"empty id", draftOwner, `{"metadata":{"draft_task_id":""}}`, "invalid_request"},
		{"non-string id", draftOwner, `{"metadata":{"draft_task_id":123}}`, "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, code := resolveDraft(t, tc.userID, tc.body)
			if code != tc.want {
				t.Fatalf("code = %q, want %q", code, tc.want)
			}
			if info.SeedanceDraftOrigin != nil {
				t.Fatal("rejected request must not resolve a draft origin")
			}
		})
	}
}

func TestResolveOriginTaskIgnoresRequestsWithoutDraftTaskID(t *testing.T) {
	info, code := resolveDraft(t, draftOwner, `{"model":"sv-seedance-2.5","prompt":"hi","metadata":{"draft":true}}`)
	if code != "" || info.OriginTaskID != "" || info.SeedanceDraftOrigin != nil {
		t.Fatalf("code = %q, origin = %q, draft = %v; want untouched", code, info.OriginTaskID, info.SeedanceDraftOrigin)
	}
}
