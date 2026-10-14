package helper

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestImageProviderBillingBeforeChannelMetaInitialization(t *testing.T) {
	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(k, v string) error { saved[k] = v; return nil }))
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_rule":        `{"channel_name:apimart:sv-gpt-image-2.5-flare":"upstream_cost"}`,
		"billing_setting.billing_rule_params": `{"channel_name:apimart:sv-gpt-image-2.5-flare":{"reserve_price_per_image":1}}`,
	}))
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/images/generations", nil)
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 7)
	common.SetContextKey(ctx, constant.ContextKeyChannelName, "apimart")
	common.SetContextKey(ctx, constant.ContextKeyChannelType, constant.ChannelTypeAPImart)
	info := &relaycommon.RelayInfo{
		OriginModelName: "sv-gpt-image-2.5-flare", UserGroup: "default", UsingGroup: "default",
		BillingRequestInput: &billingexpr.RequestInput{Body: []byte(`{"n":2}`)},
	}
	price, err := ModelPriceHelper(ctx, info, 21, &types.TokenCountMeta{})
	require.NoError(t, err)
	require.Equal(t, 2.0, price.ModelPrice)
	require.NotNil(t, info.BillingCalcSnapshot)
	require.Equal(t, "upstream_cost", info.BillingCalcSnapshot.RuleName)
	require.Equal(t, 7, info.BillingCalcSnapshot.ChannelID)
	require.Equal(t, "apimart", info.BillingCalcSnapshot.ChannelName)
	require.Equal(t, constant.ChannelTypeAPImart, info.BillingCalcSnapshot.ChannelType)
	require.Nil(t, info.ChannelMeta, "pricing must not change channel-selection retry behavior")
}
