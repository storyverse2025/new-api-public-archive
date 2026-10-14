package billingcalc

import (
	"fmt"
	"math"

	"github.com/tidwall/gjson"
)

// UpstreamCostRule reserves a configurable amount per requested image, then
// settles against APIMart's actual USD cost (which already includes all images,
// input tokens, cached tokens, and account discounts).
type UpstreamCostRule struct{}

func (UpstreamCostRule) Name() string { return "upstream_cost" }

func (r UpstreamCostRule) Estimate(ctx Context) (Result, error) {
	reserve := paramFloat(ctx.RuleParams, "reserve_price_per_image", 0)
	multiplier := paramFloat(ctx.RuleParams, "cost_multiplier", 1)
	if !positiveFinite(reserve) || !positiveFinite(multiplier) {
		return Result{}, fmt.Errorf("%s requires finite reserve_price_per_image and cost_multiplier > 0", r.Name())
	}
	n := jsonFloat(ctx.RequestBody, 1, "n")
	if n <= 0 {
		n = 1
	}
	cost := reserve * n * multiplier
	if !positiveFinite(cost) {
		return Result{}, fmt.Errorf("%s invalid reservation", r.Name())
	}
	return Result{
		RuleName: r.Name(), CostUSD: cost,
		Params:    map[string]any{"n": n, "cost_multiplier": multiplier},
		Breakdown: []Line{{Name: "reservation", Units: n, RateUSD: reserve * multiplier, CostUSD: cost}},
	}, nil
}

func (r UpstreamCostRule) Settle(ctx Context) (Result, error) {
	// credits_cost is in APIMart credits, not USD. Never substitute it for cost.
	value := gjson.GetBytes(ctx.ResponseBody, "data.cost")
	if value.Type != gjson.Number || value.Float() < 0 || math.IsNaN(value.Float()) || math.IsInf(value.Float(), 0) {
		return Result{}, fmt.Errorf("%s requires a non-negative finite data.cost in USD", r.Name())
	}
	multiplier := paramFloat(ctx.RuleParams, "cost_multiplier", 1)
	if !positiveFinite(multiplier) {
		return Result{}, fmt.Errorf("%s invalid cost_multiplier", r.Name())
	}
	cost := value.Float() * multiplier
	if math.IsNaN(cost) || math.IsInf(cost, 0) {
		return Result{}, fmt.Errorf("%s invalid settled cost", r.Name())
	}
	return Result{
		RuleName: r.Name(), CostUSD: cost,
		Params:    map[string]any{"upstream_cost_usd": value.Float(), "cost_multiplier": multiplier},
		Breakdown: []Line{{Name: "upstream_cost", Units: 1, RateUSD: cost, CostUSD: cost}},
	}, nil
}

func positiveFinite(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
