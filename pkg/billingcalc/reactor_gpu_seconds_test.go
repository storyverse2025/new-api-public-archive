package billingcalc

import (
	"math"
	"testing"
)

func TestReactorBillingReservesSessionCapNotOutputDuration(t *testing.T) {
	estimated, err := Estimate("reactor_gpu_seconds", Context{
		RequestBody: []byte(`{"seconds":8,"metadata":{"seconds":5.875}}`),
		GroupRatio:  1, QuotaPerUnit: 500000,
		RuleParams: map[string]any{"reserve_session_seconds": 120.0, "price_per_second": 0.0125},
	})
	if err != nil {
		t.Fatal(err)
	}
	if estimated.CostUSD != 1.5 || estimated.Quota != 750000 {
		t.Fatalf("must reserve the capped GPU session: %+v", estimated)
	}
	settled, err := SettleSnapshot(estimated.Snapshot, Context{
		ResponseBody: []byte(`{"data":{"duration_seconds":9.75,"output_duration_seconds":5.875}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(settled.CostUSD-0.121875) > 1e-12 || settled.Quota != 60938 {
		t.Fatalf("must use GPU seconds with fractional precision: %+v", settled)
	}
}

func TestReactorBillingRequiresGPUSeconds(t *testing.T) {
	estimated, err := Estimate("reactor_gpu_seconds", Context{RuleParams: map[string]any{"reserve_session_seconds": 120.0, "price_per_second": 0.0125}})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{}`, `{"data":{"output_duration_seconds":8}}`, `{"data":{"duration_seconds":-1}}`, `{"data":{"duration_seconds":null}}`} {
		if _, err := SettleSnapshot(estimated.Snapshot, Context{ResponseBody: []byte(body)}); err == nil {
			t.Fatalf("accepted invalid GPU meter: %s", body)
		}
	}
	settled, err := SettleSnapshot(estimated.Snapshot, Context{ResponseBody: []byte(`{"data":{"duration_seconds":0}}`)})
	if err != nil || settled.CostUSD != 0 {
		t.Fatalf("zero meter: %+v, %v", settled, err)
	}
}

func TestReactorBillingRejectsUnboundedSession(t *testing.T) {
	for _, cap := range []float64{0, -1, 121, math.Inf(1)} {
		if _, err := Estimate("reactor_gpu_seconds", Context{RuleParams: map[string]any{"reserve_session_seconds": cap, "price_per_second": 0.0125}}); err == nil {
			t.Fatalf("accepted session cap %v", cap)
		}
	}
}
