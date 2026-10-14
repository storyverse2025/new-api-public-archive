package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingcalc"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

func TestGPTImage25TaskRefundsToActualUpstreamCost(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		quota      int
	}{
		{"paid", `{"data":{"cost":0.01325,"credits_cost":0.1325}}`, 6625},
		{"free", `{"data":{"cost":0}}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			seedUser(t, 40, 1000000)
			seedToken(t, 40, 40, "test-gpt25-token", 1000000)
			seedChannel(t, 40)
			estimated, err := billingcalc.Estimate("upstream_cost", billingcalc.Context{
				RequestBody: []byte(`{"n":2}`), RuleParams: map[string]any{"reserve_price_per_image": 1.0},
				GroupRatio: 1, QuotaPerUnit: 500000,
			})
			require.NoError(t, err)
			task := makeTask(40, 40, estimated.Quota, 40, BillingSourceWallet, 0)
			task.PrivateData.BillingContext.ProviderBillingSnapshot = estimated.Snapshot
			require.NoError(t, task.Insert())
			model.UpdateUserUsedQuotaAndRequestCount(40, estimated.Quota)
			model.UpdateChannelUsedQuota(40, estimated.Quota)
			require.True(t, RecalculateTaskQuotaByProviderBilling(context.Background(), task, &relaycommon.TaskInfo{ResponseBody: []byte(tc.body)}))
			require.Equal(t, tc.quota, task.Quota)
			require.Equal(t, 1000000+estimated.Quota-tc.quota, getUserQuota(t, 40))
			require.Equal(t, 1000000+estimated.Quota-tc.quota, getTokenRemainQuota(t, 40))
			var stored model.Task
			require.NoError(t, model.DB.First(&stored, task.ID).Error)
			require.Equal(t, tc.quota, stored.Quota)
			require.Equal(t, tc.quota, stored.PrivateData.BillingContext.ProviderBillingSnapshot.EstimatedQuota)
			used, err := model.GetUserUsedQuota(40)
			require.NoError(t, err)
			require.Equal(t, tc.quota, used)
			var user model.User
			require.NoError(t, model.DB.First(&user, 40).Error)
			require.Equal(t, 1, user.RequestCount)
			var ch model.Channel
			require.NoError(t, model.DB.First(&ch, 40).Error)
			require.Equal(t, int64(tc.quota), ch.UsedQuota)
			var other map[string]any
			log := getLastLog(t)
			require.NotNil(t, log)
			require.NoError(t, common.Unmarshal([]byte(log.Other), &other))
			require.Equal(t, float64(tc.quota), other["actual_quota"])
		})
	}
}

func TestProviderCostRefreshesLogWhenQuotaDeltaIsZero(t *testing.T) {
	truncate(t)
	seedUser(t, 41, 1000)
	seedChannel(t, 41)
	estimated, err := billingcalc.Estimate("upstream_cost", billingcalc.Context{
		GroupRatio: 0, QuotaPerUnit: 500000, RuleParams: map[string]any{"reserve_price_per_image": 1.0},
	})
	require.NoError(t, err)
	task := makeTask(41, 41, 0, 0, BillingSourceWallet, 0)
	task.PrivateData.BillingContext.ProviderBillingSnapshot = estimated.Snapshot
	log := &model.Log{UserId: 41, Type: model.LogTypeConsume, Quota: 0, Other: `{"billing_cost_usd":1}`}
	require.NoError(t, model.LOG_DB.Create(log).Error)
	task.PrivateData.ConsumeLogId = log.Id
	require.NoError(t, task.Insert())
	require.True(t, RecalculateTaskQuotaByProviderBilling(context.Background(), task, &relaycommon.TaskInfo{ResponseBody: []byte(`{"data":{"cost":0.01325}}`)}))
	var refreshed model.Log
	require.NoError(t, model.LOG_DB.First(&refreshed, log.Id).Error)
	var other map[string]any
	require.NoError(t, common.Unmarshal([]byte(refreshed.Other), &other))
	require.Equal(t, 0.01325, other["billing_cost_usd"])
	require.Equal(t, 0, refreshed.Quota)
}
