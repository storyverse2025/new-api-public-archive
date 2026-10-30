package billingcalc

import (
	"math"
	"testing"
)

func TestFixedImageRule(t *testing.T) {
	result, err := Estimate("fixed_image", Context{
		GroupRatio:   1,
		QuotaPerUnit: 500000,
		RequestBody:  []byte(`{"n":3}`),
		RuleParams: map[string]any{
			"price_per_image": 0.035,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(result.CostUSD-0.105) > 1e-12 {
		t.Fatalf("cost = %v, want 0.105", result.CostUSD)
	}
	if result.Quota != 52500 {
		t.Fatalf("quota = %d, want 52500", result.Quota)
	}
}

func imageQualityResolutionParams() map[string]any {
	return map[string]any{
		"default_quality":    "medium",
		"default_resolution": "1k",
		"price_low_1k":       0.00304,
		"price_low_2k":       0.00392,
		"price_low_4k":       0.00904,
		"price_medium_1k":    0.026,
		"price_medium_2k":    0.03408,
		"price_medium_4k":    0.08024,
		"price_high_1k":      0.1036,
		"price_high_2k":      0.13576,
		"price_high_4k":      0.32032,
	}
}

func TestImageQualityResolutionExplicit(t *testing.T) {
	result, err := Estimate("image_quality_resolution", Context{
		GroupRatio:   1,
		QuotaPerUnit: 500000,
		RequestBody:  []byte(`{"quality":"high","resolution":"4k","n":2}`),
		RuleParams:   imageQualityResolutionParams(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(result.CostUSD-0.64064) > 1e-9 { // 0.32032 * 2
		t.Fatalf("cost = %v, want 0.64064", result.CostUSD)
	}
}

func TestImageQualityResolutionSizeDerivesTier(t *testing.T) {
	// 2048x1152 ≈ 2.36M px -> 2k tier; default quality (medium) since none given.
	result, err := Estimate("image_quality_resolution", Context{
		GroupRatio:   1,
		QuotaPerUnit: 500000,
		RequestBody:  []byte(`{"size":"2048x1152"}`),
		RuleParams:   imageQualityResolutionParams(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(result.CostUSD-0.03408) > 1e-9 { // medium_2k
		t.Fatalf("cost = %v, want 0.03408 (medium_2k)", result.CostUSD)
	}
}

func TestImageQualityResolutionAutoFallsBackToDefault(t *testing.T) {
	// quality "auto" is not a priced tier -> default_quality (medium); resolution 1k.
	result, err := Estimate("image_quality_resolution", Context{
		GroupRatio:   1,
		QuotaPerUnit: 500000,
		RequestBody:  []byte(`{"quality":"auto","resolution":"1k"}`),
		RuleParams:   imageQualityResolutionParams(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(result.CostUSD-0.026) > 1e-9 { // medium_1k
		t.Fatalf("cost = %v, want 0.026 (medium_1k)", result.CostUSD)
	}
}

func TestImageQualityResolutionSettleUsesSnapshot(t *testing.T) {
	snapshot := &Snapshot{
		RuleName:     "image_quality_resolution",
		GroupRatio:   1,
		QuotaPerUnit: 500000,
		RuleParams:   imageQualityResolutionParams(),
		Params:       map[string]any{"quality": "low", "resolution": "4k", "n": float64(3)},
	}
	result, err := SettleSnapshot(snapshot, Context{})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(result.CostUSD-0.02712) > 1e-9 { // 0.00904 * 3
		t.Fatalf("cost = %v, want 0.02712 (low_4k x3)", result.CostUSD)
	}
}

func TestFixedPriceRule(t *testing.T) {
	result, err := Estimate("fixed_price", Context{
		GroupRatio:   1.5,
		QuotaPerUnit: 500000,
		RuleParams: map[string]any{
			"price": 0.02,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(result.CostUSD-0.02) > 1e-12 {
		t.Fatalf("cost = %v, want 0.02", result.CostUSD)
	}
	if result.Quota != 15000 {
		t.Fatalf("quota = %d, want 15000", result.Quota)
	}
}

func TestPerSecondRuleAudioOn(t *testing.T) {
	result, err := Estimate("per_second", Context{
		GroupRatio:   1,
		QuotaPerUnit: 500000,
		RequestBody:  []byte(`{"duration":5,"metadata":{"generate_audio":true}}`),
		RuleParams: map[string]any{
			"audio_off_price_per_second": 0.112,
			"audio_on_price_per_second":  0.14,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(result.CostUSD-0.7) > 1e-12 {
		t.Fatalf("cost = %v, want 0.7", result.CostUSD)
	}
	if result.Quota != 350000 {
		t.Fatalf("quota = %d, want 350000", result.Quota)
	}
}

func TestPerSecondRuleResolutionSpecificRate(t *testing.T) {
	result, err := Estimate("per_second", Context{
		GroupRatio:   1,
		QuotaPerUnit: 500000,
		RequestBody:  []byte(`{"duration":5,"resolution":"4k","generate_audio":false}`),
		RuleParams: map[string]any{
			"audio_off_price_per_second":    0.2,
			"audio_off_4k_price_per_second": 0.4,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(result.CostUSD-2.0) > 1e-12 {
		t.Fatalf("cost = %v, want 2.0", result.CostUSD)
	}
}

func TestPerSecondSettleUsesResponseDuration(t *testing.T) {
	estimated, err := Estimate("per_second", Context{
		GroupRatio:   1,
		QuotaPerUnit: 500000,
		RequestBody:  []byte(`{"duration":5,"resolution":"4k","generate_audio":false}`),
		RuleParams: map[string]any{
			"audio_off_4k_price_per_second": 0.4,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	settled, err := SettleSnapshot(estimated.Snapshot, Context{
		ResponseBody: []byte(`{"duration":8,"resolution":"4k"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(settled.CostUSD-3.2) > 1e-12 {
		t.Fatalf("cost = %v, want 3.2", settled.CostUSD)
	}
}

func TestPerSecondRuleDurationSeconds(t *testing.T) {
	result, err := Estimate("per_second", Context{
		GroupRatio:   1,
		QuotaPerUnit: 500000,
		RequestBody:  []byte(`{"text":"cinematic whoosh","duration_seconds":2.5}`),
		RuleParams: map[string]any{
			"price_per_second": 0.002,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(result.CostUSD-0.005) > 1e-12 {
		t.Fatalf("cost = %v, want 0.005", result.CostUSD)
	}
}

func TestPerCharacterRule(t *testing.T) {
	result, err := Estimate("per_character", Context{
		GroupRatio:   1,
		QuotaPerUnit: 500000,
		RequestBody:  []byte(`{"input":"hello 世界"}`),
		RuleParams: map[string]any{
			"price_per_1k_characters": 0.1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(result.CostUSD-0.0008) > 1e-12 {
		t.Fatalf("cost = %v, want 0.0008", result.CostUSD)
	}
}

func TestBytePlusSeedance2Rule720pNoVideo(t *testing.T) {
	result, err := Estimate("byteplus_seedance2", Context{
		GroupRatio:   1,
		QuotaPerUnit: 500000,
		RequestBody:  []byte(`{"duration":5,"metadata":{"resolution":"720p"}}`),
		RuleParams: map[string]any{
			"fps": 24,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCost := (5.0 * 1280 * 720 * 24 / 1024) / 1_000_000 * 7.0
	if math.Abs(result.CostUSD-wantCost) > 1e-12 {
		t.Fatalf("cost = %v, want %v", result.CostUSD, wantCost)
	}
	if result.Quota != QuotaFromUSD(wantCost, 500000, 1) {
		t.Fatalf("quota = %d, want %d", result.Quota, QuotaFromUSD(wantCost, 500000, 1))
	}
}

func TestBytePlusSeedance2SettleUsesResponseDuration(t *testing.T) {
	estimated, err := Estimate("byteplus_seedance2", Context{
		GroupRatio:   1,
		QuotaPerUnit: 500000,
		RequestBody:  []byte(`{"duration":-1,"metadata":{"resolution":"720p"}}`),
		RuleParams: map[string]any{
			"default_seconds": 5,
			"fps":             24,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	settled, err := SettleSnapshot(estimated.Snapshot, Context{
		ResponseBody: []byte(`{"status":"succeeded","duration":8,"resolution":"720p","framespersecond":24}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCost := (8.0 * 1280 * 720 * 24 / 1024) / 1_000_000 * 7.0
	if math.Abs(settled.CostUSD-wantCost) > 1e-12 {
		t.Fatalf("cost = %v, want %v", settled.CostUSD, wantCost)
	}
	if settled.Quota <= estimated.Quota {
		t.Fatalf("settled quota = %d, estimated quota = %d", settled.Quota, estimated.Quota)
	}
}

func TestSnapshotApplySettlementRefreshesEstimate(t *testing.T) {
	// Pre-consume at the default 5s estimate.
	estimated, err := Estimate("byteplus_seedance2", Context{
		GroupRatio:   1,
		QuotaPerUnit: 500000,
		RequestBody:  []byte(`{"duration":-1,"metadata":{"resolution":"720p"}}`),
		RuleParams: map[string]any{
			"default_seconds": 5,
			"fps":             24,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	snap := estimated.Snapshot

	// Upstream actually produced 8s — settle against the real response.
	settled, err := SettleSnapshot(snap, Context{
		ResponseBody: []byte(`{"status":"succeeded","duration":8,"resolution":"720p","framespersecond":24}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Before refresh the snapshot still holds the 5s estimate.
	if snap.EstimatedQuota != estimated.Quota {
		t.Fatalf("pre-refresh snapshot quota = %d, want estimate %d", snap.EstimatedQuota, estimated.Quota)
	}

	snap.ApplySettlement(settled)

	if snap.EstimatedCostUSD != settled.CostUSD {
		t.Fatalf("snapshot cost = %v, want settled %v", snap.EstimatedCostUSD, settled.CostUSD)
	}
	if snap.EstimatedQuota != settled.Quota {
		t.Fatalf("snapshot quota = %d, want settled %d", snap.EstimatedQuota, settled.Quota)
	}
	if got := snap.Params["output_seconds"]; got != settled.Params["output_seconds"] {
		t.Fatalf("snapshot params output_seconds = %v, want %v", got, settled.Params["output_seconds"])
	}
	if len(snap.Breakdown) == 0 || snap.Breakdown[0].CostUSD != settled.CostUSD {
		t.Fatalf("snapshot breakdown not refreshed: %+v", snap.Breakdown)
	}
}

func TestSettleSnapshotUsesStoredParams(t *testing.T) {
	estimated, err := Estimate("fixed_image", Context{
		GroupRatio:   2,
		QuotaPerUnit: 500000,
		RequestBody:  []byte(`{"n":2}`),
		RuleParams: map[string]any{
			"price_per_image": 0.035,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	settled, err := SettleSnapshot(estimated.Snapshot, Context{})
	if err != nil {
		t.Fatal(err)
	}
	if settled.Quota != estimated.Quota {
		t.Fatalf("settled quota = %d, want %d", settled.Quota, estimated.Quota)
	}
}

// seedance25RuleParams mirrors deploy/seed_billing_rules.sh for sv-seedance-2.5.
func seedance25RuleParams() map[string]any {
	return map[string]any{
		"default_seconds":    5,
		"default_resolution": "720p",
		"fps":                24,
		"width_480p":         854,
		"height_480p":        480,
		"standard_720p_no_video_rate_per_m_tokens":     10.7,
		"standard_1080p_no_video_rate_per_m_tokens":    11.7,
		"standard_720p_video_input_rate_per_m_tokens":  6.4,
		"standard_1080p_video_input_rate_per_m_tokens": 7.0,
		"settle_by_completion_tokens":                  true,
	}
}

// BytePlus pricing examples for Dreamina Seedance 2.5 (16:9, 5s, no video input).
func TestBytePlusSeedance25MatchesOfficialExamples(t *testing.T) {
	for _, tc := range []struct {
		resolution   string
		perSecondUSD float64
	}{
		{"480p", 0.103},
		{"720p", 0.231},
		{"1080p", 0.569},
	} {
		t.Run(tc.resolution, func(t *testing.T) {
			result, err := Estimate("byteplus_seedance2", Context{
				GroupRatio:   1,
				QuotaPerUnit: 500000,
				RequestBody:  []byte(`{"metadata":{"duration":5,"resolution":"` + tc.resolution + `"}}`),
				RuleParams:   seedance25RuleParams(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := result.CostUSD / 5; math.Abs(got-tc.perSecondUSD) > 0.0005 {
				t.Fatalf("cost per second = %v, want %v", got, tc.perSecondUSD)
			}
		})
	}
}

func TestBytePlusSeedance2DetectsVideoInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"top-level videos", `{"videos":["https://example.com/v.mp4"],"metadata":{"resolution":"720p"}}`, true},
		{"empty videos", `{"videos":[],"metadata":{"resolution":"720p"}}`, false},
		{"explicit flag", `{"has_video_input":true,"resolution":"1080p"}`, true},
		{"images only", `{"images":["https://example.com/a.png"],"metadata":{"resolution":"720p"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Estimate("byteplus_seedance2", Context{
				GroupRatio:   1,
				QuotaPerUnit: 500000,
				RequestBody:  []byte(tc.body),
				RuleParams:   seedance25RuleParams(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Params["has_video_input"]; got != tc.want {
				t.Fatalf("has_video_input = %v, want %v", got, tc.want)
			}
		})
	}
}

// A final video from a draft is billed at the 1080p video-input rate when the
// draft had video input, over the draft's input and output seconds.
func TestBytePlusSeedance25DraftFinalUsesDraftVideoInput(t *testing.T) {
	result, err := Estimate("byteplus_seedance2", Context{
		GroupRatio:   1,
		QuotaPerUnit: 500000,
		RequestBody:  []byte(`{"resolution":"1080p","duration":6,"has_video_input":true,"input_video_duration":4}`),
		RuleParams:   seedance25RuleParams(),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCost := (10.0 * 1920 * 1080 * 24 / 1024) / 1_000_000 * 7.0
	if math.Abs(result.CostUSD-wantCost) > 1e-12 {
		t.Fatalf("cost = %v, want %v", result.CostUSD, wantCost)
	}
}

func TestBytePlusSeedance2SettleByCompletionTokens(t *testing.T) {
	estimated, err := Estimate("byteplus_seedance2", Context{
		GroupRatio:   1,
		QuotaPerUnit: 500000,
		RequestBody:  []byte(`{"videos":["https://example.com/v.mp4"],"metadata":{"resolution":"720p","duration":5}}`),
		RuleParams:   seedance25RuleParams(),
	})
	if err != nil {
		t.Fatal(err)
	}
	settled, err := SettleSnapshot(estimated.Snapshot, Context{
		ResponseBody: []byte(`{"status":"succeeded","duration":5,"resolution":"720p","usage":{"completion_tokens":250000}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCost := 250000.0 / 1_000_000 * 6.4
	if math.Abs(settled.CostUSD-wantCost) > 1e-12 {
		t.Fatalf("cost = %v, want %v", settled.CostUSD, wantCost)
	}

	// Without the flag the formula still applies (Seedance 2.0 behaviour).
	params := seedance25RuleParams()
	delete(params, "settle_by_completion_tokens")
	estimated, err = Estimate("byteplus_seedance2", Context{
		GroupRatio:   1,
		QuotaPerUnit: 500000,
		RequestBody:  []byte(`{"metadata":{"resolution":"720p","duration":5}}`),
		RuleParams:   params,
	})
	if err != nil {
		t.Fatal(err)
	}
	settled, err = SettleSnapshot(estimated.Snapshot, Context{
		ResponseBody: []byte(`{"status":"succeeded","duration":5,"resolution":"720p","usage":{"completion_tokens":250000}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(settled.CostUSD-estimated.CostUSD) > 1e-12 {
		t.Fatalf("cost = %v, want formula cost %v", settled.CostUSD, estimated.CostUSD)
	}
}
