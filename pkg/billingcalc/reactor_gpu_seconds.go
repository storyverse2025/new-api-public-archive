package billingcalc

import (
	"fmt"
	"math"

	"github.com/tidwall/gjson"
)

// Reactor bills the GPU session, not the duration of the generated clip.
// Reserve the worker's capped session lifetime, then settle its measured meter.
type ReactorGPUSecondsRule struct{}

func (ReactorGPUSecondsRule) Name() string { return "reactor_gpu_seconds" }

func (r ReactorGPUSecondsRule) Estimate(ctx Context) (Result, error) {
	seconds := paramFloat(ctx.RuleParams, "reserve_session_seconds", 0)
	rate := paramFloat(ctx.RuleParams, "price_per_second", 0)
	if !positiveFinite(seconds) || seconds > 120 || !positiveFinite(rate) {
		return Result{}, fmt.Errorf("%s requires reserve_session_seconds in (0,120] and finite price_per_second > 0", r.Name())
	}
	return reactorGPUResult(r.Name(), seconds, rate), nil
}

func (r ReactorGPUSecondsRule) Settle(ctx Context) (Result, error) {
	value := gjson.GetBytes(ctx.ResponseBody, "data.duration_seconds")
	seconds := value.Float()
	rate := paramFloat(ctx.RuleParams, "price_per_second", 0)
	if value.Type != gjson.Number || seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) || !positiveFinite(rate) {
		return Result{}, fmt.Errorf("%s requires non-negative finite GPU data.duration_seconds and finite price_per_second > 0", r.Name())
	}
	if math.IsInf(seconds*rate, 0) {
		return Result{}, fmt.Errorf("%s invalid cost", r.Name())
	}
	return reactorGPUResult(r.Name(), seconds, rate), nil
}

func reactorGPUResult(name string, seconds, rate float64) Result {
	cost := seconds * rate
	return Result{
		RuleName: name, CostUSD: cost,
		Params:    map[string]any{"gpu_seconds": seconds},
		Breakdown: []Line{{Name: "gpu_seconds", Units: seconds, RateUSD: rate, CostUSD: cost}},
	}
}
