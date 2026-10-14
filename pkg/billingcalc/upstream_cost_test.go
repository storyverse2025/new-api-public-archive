package billingcalc

import (
	"math"
	"testing"
)

func TestUpstreamCostReservationAndSettlement(t *testing.T) {
	estimate, err := Estimate("upstream_cost", Context{
		RequestBody: []byte(`{"n":2,"quality":"max","resolution":"4k"}`),
		GroupRatio:  1.5, QuotaPerUnit: 500000,
		RuleParams: map[string]any{"reserve_price_per_image": 1.0, "cost_multiplier": 1.2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if estimate.CostUSD != 2.4 || estimate.Quota != 1800000 {
		t.Fatalf("unexpected reservation: %+v", estimate)
	}
	settled, err := SettleSnapshot(estimate.Snapshot, Context{
		ResponseBody: []byte(`{"code":200,"data":{"cost":0.01325,"credits_cost":0.1325,"usage":{"total_tokens":455}}}`),
		RuleParams:   map[string]any{"cost_multiplier": 99.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(settled.CostUSD-0.0159) > 1e-12 || settled.Quota != 11925 {
		t.Fatalf("must settle actual USD once with frozen multiplier: %+v", settled)
	}
}

func TestUpstreamCostRejectsMissingOrInvalidCost(t *testing.T) {
	estimate, err := Estimate("upstream_cost", Context{RuleParams: map[string]any{"reserve_price_per_image": 1.0}})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{}`, `{"data":{"credits_cost":0.1325}}`, `{"data":{"cost":null}}`, `{"data":{"cost":-1}}`, `{"data":{"cost":"bad"}}`} {
		t.Run(body, func(t *testing.T) {
			if _, err := SettleSnapshot(estimate.Snapshot, Context{ResponseBody: []byte(body)}); err == nil {
				t.Fatal("must not silently charge the reserve or interpret credits as dollars")
			}
		})
	}
	settled, err := SettleSnapshot(estimate.Snapshot, Context{ResponseBody: []byte(`{"data":{"cost":0}}`)})
	if err != nil || settled.CostUSD != 0 || settled.Quota != 0 {
		t.Fatalf("zero cost must be preserved: %+v, %v", settled, err)
	}
}

func TestUpstreamCostRejectsInvalidReservation(t *testing.T) {
	for _, params := range []map[string]any{
		{}, {"reserve_price_per_image": -1.0}, {"reserve_price_per_image": math.Inf(1)},
		{"reserve_price_per_image": 1.0, "cost_multiplier": -1.0},
	} {
		if _, err := Estimate("upstream_cost", Context{RuleParams: params}); err == nil {
			t.Fatalf("accepted invalid params: %+v", params)
		}
	}
}
