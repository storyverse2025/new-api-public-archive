package billingcalc

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

func init() {
	Register(FixedPriceRule{})
	Register(FixedImageRule{})
	Register(ImageQualityResolutionRule{})
	Register(UpstreamCostRule{})
	Register(ReactorGPUSecondsRule{})
	Register(PerSecondRule{})
	Register(PerCharacterRule{})
	Register(BytePlusSeedance2Rule{})
}

type FixedPriceRule struct{}

func (FixedPriceRule) Name() string { return "fixed_price" }

func (r FixedPriceRule) Estimate(ctx Context) (Result, error) {
	price := paramFloat(ctx.RuleParams, "price", paramFloat(ctx.RuleParams, "price_per_call", 0))
	if price <= 0 {
		return Result{}, fmt.Errorf("%s requires price or price_per_call > 0", r.Name())
	}
	return Result{
		RuleName:  r.Name(),
		CostUSD:   price,
		Params:    map[string]any{"units": 1.0},
		Breakdown: []Line{{Name: "call", Units: 1, RateUSD: price, CostUSD: price}},
	}, nil
}

func (r FixedPriceRule) Settle(ctx Context) (Result, error) {
	return r.Estimate(ctx)
}

type FixedImageRule struct{}

func (FixedImageRule) Name() string { return "fixed_image" }

func (r FixedImageRule) Estimate(ctx Context) (Result, error) {
	price := paramFloat(ctx.RuleParams, "price_per_image", 0)
	if price <= 0 {
		return Result{}, fmt.Errorf("%s requires price_per_image > 0", r.Name())
	}
	n := jsonFloat(ctx.RequestBody, 1, "n")
	if n <= 0 {
		n = 1
	}
	cost := price * n
	return Result{
		RuleName: r.Name(),
		CostUSD:  cost,
		Params: map[string]any{
			"n": n,
		},
		Breakdown: []Line{{Name: "image", Units: n, RateUSD: price, CostUSD: cost}},
	}, nil
}

func (r FixedImageRule) Settle(ctx Context) (Result, error) {
	if ctx.Snapshot != nil && len(ctx.Snapshot.Params) > 0 {
		ctx.RequestBody = nil
		ctx.RuleParams = ctx.Snapshot.RuleParams
		n := paramFloat(ctx.Snapshot.Params, "n", 1)
		price := paramFloat(ctx.RuleParams, "price_per_image", 0)
		cost := price * n
		return Result{
			RuleName:  r.Name(),
			CostUSD:   cost,
			Params:    cloneMap(ctx.Snapshot.Params),
			Breakdown: []Line{{Name: "image", Units: n, RateUSD: price, CostUSD: cost}},
		}, nil
	}
	return r.Estimate(ctx)
}

// ImageQualityResolutionRule prices an image generation by a quality × resolution
// matrix (e.g. APIMart gpt-image-2-official). The 9 cell prices live in RuleParams
// under keys `price_{quality}_{resolution}` where quality ∈ {low,medium,high} and
// resolution ∈ {1k,2k,4k}. Quality is read from the request `quality` field and
// resolution from `resolution` (preferred) or derived from a pixel `size`. The
// non-priced `auto`/absent quality falls back to `default_quality`; an absent
// resolution falls back to `default_resolution`. Cost = matched cell price × n.
type ImageQualityResolutionRule struct{}

func (ImageQualityResolutionRule) Name() string { return "image_quality_resolution" }

func (r ImageQualityResolutionRule) Estimate(ctx Context) (Result, error) {
	quality := normalizeImageQuality(
		jsonString(ctx.RequestBody, "", "quality", "metadata.quality"),
		paramString(ctx.RuleParams, "default_quality", "medium"),
	)
	resolution := normalizeImageResolution(
		jsonString(ctx.RequestBody, "", "resolution", "metadata.resolution"),
		jsonString(ctx.RequestBody, "", "size", "metadata.size"),
		paramString(ctx.RuleParams, "default_resolution", "1k"),
	)
	n := jsonFloat(ctx.RequestBody, 1, "n")
	if n <= 0 {
		n = 1
	}
	price, key := imageQualityResolutionPrice(ctx.RuleParams, quality, resolution)
	if price <= 0 {
		return Result{}, fmt.Errorf("%s requires %s > 0", r.Name(), key)
	}
	cost := price * n
	return Result{
		RuleName: r.Name(),
		CostUSD:  cost,
		Params: map[string]any{
			"quality":    quality,
			"resolution": resolution,
			"n":          n,
		},
		Breakdown: []Line{{Name: quality + "_" + resolution, Units: n, RateUSD: price, CostUSD: cost}},
	}, nil
}

func (r ImageQualityResolutionRule) Settle(ctx Context) (Result, error) {
	if ctx.Snapshot != nil && len(ctx.Snapshot.Params) > 0 {
		quality := paramString(ctx.Snapshot.Params, "quality", paramString(ctx.RuleParams, "default_quality", "medium"))
		resolution := paramString(ctx.Snapshot.Params, "resolution", paramString(ctx.RuleParams, "default_resolution", "1k"))
		n := paramFloat(ctx.Snapshot.Params, "n", 1)
		price, key := imageQualityResolutionPrice(ctx.RuleParams, quality, resolution)
		if price <= 0 {
			return Result{}, fmt.Errorf("%s requires %s > 0", r.Name(), key)
		}
		cost := price * n
		return Result{
			RuleName:  r.Name(),
			CostUSD:   cost,
			Params:    cloneMap(ctx.Snapshot.Params),
			Breakdown: []Line{{Name: quality + "_" + resolution, Units: n, RateUSD: price, CostUSD: cost}},
		}, nil
	}
	return r.Estimate(ctx)
}

// normalizeImageQuality maps a request quality value to a priced tier. The
// provider's `auto` (and any absent/unknown value) is not a priced tier, so it
// falls back to the configured default quality.
func normalizeImageQuality(value, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low":
		return "low"
	case "medium", "mid", "standard":
		return "medium"
	case "high", "hd":
		return "high"
	}
	switch strings.ToLower(strings.TrimSpace(fallback)) {
	case "low":
		return "low"
	case "high":
		return "high"
	default:
		return "medium"
	}
}

// normalizeImageResolution resolves a 1k/2k/4k tier from an explicit resolution
// value (preferred), a pixel `size` (e.g. "2048x1152"), or the configured default.
// An aspect-ratio size like "16:9" is not pixel dimensions, so it is ignored here
// (callers should send an explicit resolution alongside it).
func normalizeImageResolution(explicit, size, fallback string) string {
	if t := imageResolutionTier(explicit); t != "" {
		return t
	}
	if w, h, ok := parseSize(size); ok {
		switch mp := w * h; {
		case mp <= 2_300_000:
			return "1k"
		case mp <= 5_000_000:
			return "2k"
		default:
			return "4k"
		}
	}
	if t := imageResolutionTier(fallback); t != "" {
		return t
	}
	return "1k"
}

func imageResolutionTier(value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	switch {
	case v == "":
		return ""
	case strings.Contains(v, "4k"), strings.Contains(v, "2160"), strings.Contains(v, "2880"), strings.Contains(v, "4096"):
		return "4k"
	case strings.Contains(v, "2k"):
		return "2k"
	case strings.Contains(v, "1k"):
		return "1k"
	default:
		return ""
	}
}

func imageQualityResolutionPrice(params map[string]any, quality, resolution string) (float64, string) {
	key := "price_" + quality + "_" + resolution
	return paramFloat(params, key, 0), key
}

type PerSecondRule struct{}

func (PerSecondRule) Name() string { return "per_second" }

func (r PerSecondRule) Estimate(ctx Context) (Result, error) {
	defaultSeconds := paramFloat(ctx.RuleParams, "default_seconds", 1)
	seconds := jsonFloat(ctx.RequestBody, defaultSeconds, "duration", "duration_seconds", "seconds", "metadata.duration", "metadata.duration_seconds", "metadata.durationSeconds")
	if seconds <= 0 {
		seconds = defaultSeconds
	}

	audio := jsonBool(ctx.RequestBody, paramBool(ctx.RuleParams, "default_generate_audio", false), "generate_audio", "audio", "metadata.generate_audio", "metadata.audio")
	resolution := normalizeResolution(jsonString(ctx.RequestBody, paramString(ctx.RuleParams, "default_resolution", ""), "resolution", "size", "metadata.resolution", "metadata.size"))
	rate := perSecondRate(ctx.RuleParams, audio, resolution)
	if rate <= 0 {
		return Result{}, fmt.Errorf("%s requires price_per_second, audio_*_price_per_second, or resolution-specific *_price_per_second > 0", r.Name())
	}

	cost := seconds * rate
	return Result{
		RuleName: r.Name(),
		CostUSD:  cost,
		Params: map[string]any{
			"seconds":        seconds,
			"generate_audio": audio,
			"resolution":     resolution,
		},
		Breakdown: []Line{{Name: "seconds", Units: seconds, RateUSD: rate, CostUSD: cost}},
	}, nil
}

func (r PerSecondRule) Settle(ctx Context) (Result, error) {
	if ctx.Snapshot != nil && len(ctx.Snapshot.Params) > 0 {
		seconds := paramFloat(ctx.Snapshot.Params, "seconds", paramFloat(ctx.RuleParams, "default_seconds", 1))
		if d := jsonFloat(ctx.ResponseBody, 0, "duration", "duration_seconds", "seconds", "metadata.duration", "metadata.duration_seconds", "response.duration", "data.duration", "data.duration_seconds"); d > 0 {
			seconds = d
		}
		audio := paramBool(ctx.Snapshot.Params, "generate_audio", paramBool(ctx.RuleParams, "default_generate_audio", false))
		audio = jsonBool(ctx.ResponseBody, audio, "generate_audio", "audio", "metadata.generate_audio", "metadata.audio", "response.generate_audio", "data.generate_audio")
		resolution := paramString(ctx.Snapshot.Params, "resolution", paramString(ctx.RuleParams, "default_resolution", ""))
		if s := jsonString(ctx.ResponseBody, "", "resolution", "size", "metadata.resolution", "metadata.size", "response.resolution", "data.resolution"); s != "" {
			resolution = s
		}
		resolution = normalizeResolution(resolution)
		rate := perSecondRate(ctx.RuleParams, audio, resolution)
		if rate <= 0 {
			return Result{}, fmt.Errorf("%s requires price_per_second, audio_*_price_per_second, or resolution-specific *_price_per_second > 0", r.Name())
		}
		cost := seconds * rate
		params := cloneMap(ctx.Snapshot.Params)
		params["seconds"] = seconds
		params["generate_audio"] = audio
		params["resolution"] = resolution
		return Result{
			RuleName:  r.Name(),
			CostUSD:   cost,
			Params:    params,
			Breakdown: []Line{{Name: "seconds", Units: seconds, RateUSD: rate, CostUSD: cost}},
		}, nil
	}
	return r.Estimate(ctx)
}

type PerCharacterRule struct{}

func (PerCharacterRule) Name() string { return "per_character" }

func (r PerCharacterRule) Estimate(ctx Context) (Result, error) {
	text := jsonString(ctx.RequestBody, "", "input", "text", "prompt", "metadata.input", "metadata.text", "metadata.prompt")
	characters := float64(len([]rune(text)))
	if characters <= 0 {
		characters = paramFloat(ctx.RuleParams, "default_characters", 0)
	}
	if characters <= 0 {
		return Result{}, fmt.Errorf("%s requires a non-empty input/text/prompt or default_characters > 0", r.Name())
	}
	billableCharacters := math.Max(characters, paramFloat(ctx.RuleParams, "minimum_characters", 0))
	ratePer1K := paramFloat(ctx.RuleParams, "price_per_1k_characters", 0)
	if ratePer1K <= 0 {
		ratePer1K = paramFloat(ctx.RuleParams, "price_per_character", 0) * 1000
	}
	if ratePer1K <= 0 {
		return Result{}, fmt.Errorf("%s requires price_per_1k_characters or price_per_character > 0", r.Name())
	}
	cost := billableCharacters / 1000 * ratePer1K
	return Result{
		RuleName: r.Name(),
		CostUSD:  cost,
		Params: map[string]any{
			"characters":          characters,
			"billable_characters": billableCharacters,
		},
		Breakdown: []Line{{Name: "characters", Units: billableCharacters, RateUSD: ratePer1K, CostUSD: cost}},
	}, nil
}

func (r PerCharacterRule) Settle(ctx Context) (Result, error) {
	if ctx.Snapshot != nil && len(ctx.Snapshot.Params) > 0 {
		characters := paramFloat(ctx.Snapshot.Params, "characters", 0)
		billableCharacters := paramFloat(ctx.Snapshot.Params, "billable_characters", math.Max(characters, paramFloat(ctx.RuleParams, "minimum_characters", 0)))
		ratePer1K := paramFloat(ctx.RuleParams, "price_per_1k_characters", 0)
		if ratePer1K <= 0 {
			ratePer1K = paramFloat(ctx.RuleParams, "price_per_character", 0) * 1000
		}
		if ratePer1K <= 0 {
			return Result{}, fmt.Errorf("%s requires price_per_1k_characters or price_per_character > 0", r.Name())
		}
		cost := billableCharacters / 1000 * ratePer1K
		return Result{
			RuleName:  r.Name(),
			CostUSD:   cost,
			Params:    cloneMap(ctx.Snapshot.Params),
			Breakdown: []Line{{Name: "characters", Units: billableCharacters, RateUSD: ratePer1K, CostUSD: cost}},
		}, nil
	}
	return r.Estimate(ctx)
}

type BytePlusSeedance2Rule struct{}

func (BytePlusSeedance2Rule) Name() string { return "byteplus_seedance2" }

func (r BytePlusSeedance2Rule) Estimate(ctx Context) (Result, error) {
	params := seedanceParams(ctx)
	rate := seedanceRatePerMillion(ctx.RuleParams, params.Resolution, params.HasVideoInput, params.Fast)
	if rate <= 0 {
		return Result{}, fmt.Errorf("%s cannot resolve token rate", r.Name())
	}

	tokens := (params.InputVideoSeconds + params.OutputSeconds) * params.Width * params.Height * params.FPS / 1024
	if minTokens := seedanceMinTokens(ctx.RuleParams, params.Resolution, params.HasVideoInput, params.Fast); minTokens > 0 && tokens < minTokens {
		tokens = minTokens
	}
	cost := tokens / 1_000_000 * rate
	lineName := params.Resolution
	if params.Fast {
		lineName += "_fast"
	}
	if params.HasVideoInput {
		lineName += "_video_input"
	} else {
		lineName += "_text_image_input"
	}

	return Result{
		RuleName: r.Name(),
		CostUSD:  cost,
		Params: map[string]any{
			"resolution":          params.Resolution,
			"width":               params.Width,
			"height":              params.Height,
			"fps":                 params.FPS,
			"output_seconds":      params.OutputSeconds,
			"input_video_seconds": params.InputVideoSeconds,
			"has_video_input":     params.HasVideoInput,
			"fast":                params.Fast,
			"tokens":              tokens,
		},
		Breakdown: []Line{{Name: lineName, Units: tokens, RateUSD: rate, CostUSD: cost}},
	}, nil
}

func (r BytePlusSeedance2Rule) Settle(ctx Context) (Result, error) {
	if ctx.Snapshot != nil && len(ctx.Snapshot.Params) > 0 {
		params := seedanceSettleParams(ctx)
		rate := seedanceRatePerMillion(ctx.RuleParams, params.Resolution, params.HasVideoInput, params.Fast)
		tokens := (params.InputVideoSeconds + params.OutputSeconds) * params.Width * params.Height * params.FPS / 1024
		if minTokens := seedanceMinTokens(ctx.RuleParams, params.Resolution, params.HasVideoInput, params.Fast); minTokens > 0 && tokens < minTokens {
			tokens = minTokens
		}
		cost := tokens / 1_000_000 * rate
		return Result{
			RuleName: r.Name(),
			CostUSD:  cost,
			Params: map[string]any{
				"resolution":          params.Resolution,
				"width":               params.Width,
				"height":              params.Height,
				"fps":                 params.FPS,
				"output_seconds":      params.OutputSeconds,
				"input_video_seconds": params.InputVideoSeconds,
				"has_video_input":     params.HasVideoInput,
				"fast":                params.Fast,
				"tokens":              tokens,
			},
			Breakdown: []Line{{Name: "seedance2", Units: tokens, RateUSD: rate, CostUSD: cost}},
		}, nil
	}
	return r.Estimate(ctx)
}

type seedanceBillingParams struct {
	Resolution        string
	Width             float64
	Height            float64
	FPS               float64
	OutputSeconds     float64
	InputVideoSeconds float64
	HasVideoInput     bool
	Fast              bool
}

func seedanceParams(ctx Context) seedanceBillingParams {
	fps := paramFloat(ctx.RuleParams, "fps", 24)
	outputSeconds := jsonFloat(ctx.RequestBody, paramFloat(ctx.RuleParams, "default_seconds", 5), "duration", "seconds", "metadata.duration", "metadata.durationSeconds")
	if outputSeconds <= 0 {
		outputSeconds = paramFloat(ctx.RuleParams, "default_seconds", 5)
	}
	inputVideoSeconds := jsonFloat(ctx.RequestBody, 0, "input_video_duration", "metadata.input_video_duration", "metadata.inputVideoDuration")
	hasVideo := inputVideoSeconds > 0 || hasVideoInput(ctx.RequestBody)
	if hasVideo && inputVideoSeconds <= 0 {
		inputVideoSeconds = paramFloat(ctx.RuleParams, "default_input_video_seconds", 0)
	}

	resolution := strings.ToLower(jsonString(ctx.RequestBody, paramString(ctx.RuleParams, "default_resolution", "720p"), "resolution", "metadata.resolution", "size"))
	width, height := seedanceResolutionDimensions(ctx.RuleParams, resolution)
	fast := paramBool(ctx.RuleParams, "fast", strings.Contains(strings.ToLower(ctx.UpstreamModelName), "fast"))

	return seedanceBillingParams{
		Resolution:        normalizeResolution(resolution),
		Width:             width,
		Height:            height,
		FPS:               fps,
		OutputSeconds:     outputSeconds,
		InputVideoSeconds: inputVideoSeconds,
		HasVideoInput:     hasVideo,
		Fast:              fast,
	}
}

func seedanceSettleParams(ctx Context) seedanceBillingParams {
	snapParams := ctx.Snapshot.Params
	resolution := paramString(snapParams, "resolution", "720p")
	if s := jsonString(ctx.ResponseBody, "", "resolution", "metadata.resolution", "response.resolution", "data.resolution"); s != "" {
		resolution = s
	}
	width, height := seedanceResolutionDimensions(ctx.RuleParams, resolution)

	fps := paramFloat(snapParams, "fps", paramFloat(ctx.RuleParams, "fps", 24))
	if f := jsonFloat(ctx.ResponseBody, 0, "framespersecond", "frames_per_second", "fps", "metadata.fps", "response.framespersecond"); f > 0 {
		fps = f
	}

	outputSeconds := paramFloat(snapParams, "output_seconds", paramFloat(ctx.RuleParams, "default_seconds", 5))
	if d := jsonFloat(ctx.ResponseBody, 0, "duration", "seconds", "metadata.duration", "response.duration", "data.duration"); d > 0 {
		outputSeconds = d
	}

	hasVideo := paramBool(snapParams, "has_video_input", false)
	inputVideoSeconds := paramFloat(snapParams, "input_video_seconds", 0)
	fast := paramBool(snapParams, "fast", false)
	if serviceTier := strings.ToLower(jsonString(ctx.ResponseBody, "", "service_tier", "metadata.service_tier", "response.service_tier")); strings.Contains(serviceTier, "fast") {
		fast = true
	}

	return seedanceBillingParams{
		Resolution:        normalizeResolution(resolution),
		Width:             width,
		Height:            height,
		FPS:               fps,
		OutputSeconds:     outputSeconds,
		InputVideoSeconds: inputVideoSeconds,
		HasVideoInput:     hasVideo,
		Fast:              fast,
	}
}

func hasVideoInput(body []byte) bool {
	if hasAny(body, "video", "input_video", "video_url", "metadata.video", "metadata.input_video", "metadata.video_url") {
		return true
	}
	content := strings.ToLower(gjsonString(body, "metadata.content"))
	return strings.Contains(content, "video_url") || strings.Contains(content, `"type":"video"`) || strings.Contains(content, `"type": "video"`)
}

func gjsonString(body []byte, path string) string {
	return jsonString(body, "", path)
}

func seedanceResolutionDimensions(params map[string]any, resolution string) (float64, float64) {
	if w, h, ok := parseSize(resolution); ok {
		return w, h
	}
	switch normalizeResolution(resolution) {
	case "480p":
		return paramFloat(params, "width_480p", 832), paramFloat(params, "height_480p", 480)
	case "1080p":
		return paramFloat(params, "width_1080p", 1920), paramFloat(params, "height_1080p", 1080)
	default:
		return paramFloat(params, "width_720p", 1280), paramFloat(params, "height_720p", 720)
	}
}

func parseSize(size string) (float64, float64, bool) {
	size = strings.TrimSpace(strings.ToLower(size))
	size = strings.ReplaceAll(size, "*", "x")
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, errW := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	h, errH := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if errW != nil || errH != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

func normalizeResolution(resolution string) string {
	resolution = strings.ToLower(strings.TrimSpace(resolution))
	if strings.Contains(resolution, "4k") || strings.Contains(resolution, "2160") {
		return "4k"
	}
	if strings.Contains(resolution, "1080") {
		return "1080p"
	}
	if strings.Contains(resolution, "480") {
		return "480p"
	}
	if resolution == "" {
		return ""
	}
	return "720p"
}

func perSecondRate(params map[string]any, audio bool, resolution string) float64 {
	rate := paramFloat(params, "price_per_second", 0)
	if audio {
		rate = paramFloat(params, "audio_on_price_per_second", rate)
	} else {
		rate = paramFloat(params, "audio_off_price_per_second", rate)
	}
	if resolution != "" {
		rate = paramFloat(params, resolution+"_price_per_second", rate)
		if audio {
			rate = paramFloat(params, "audio_on_"+resolution+"_price_per_second", rate)
		} else {
			rate = paramFloat(params, "audio_off_"+resolution+"_price_per_second", rate)
		}
	}
	return rate
}

func seedanceRatePerMillion(params map[string]any, resolution string, hasVideoInput, fast bool) float64 {
	if fast {
		if hasVideoInput {
			return paramFloat(params, "fast_video_input_rate_per_m_tokens", 3.3)
		}
		return paramFloat(params, "fast_no_video_rate_per_m_tokens", 5.6)
	}
	if normalizeResolution(resolution) == "1080p" {
		if hasVideoInput {
			return paramFloat(params, "standard_1080p_video_input_rate_per_m_tokens", 4.7)
		}
		return paramFloat(params, "standard_1080p_no_video_rate_per_m_tokens", 7.7)
	}
	if hasVideoInput {
		return paramFloat(params, "standard_720p_video_input_rate_per_m_tokens", 4.3)
	}
	return paramFloat(params, "standard_720p_no_video_rate_per_m_tokens", 7.0)
}

func seedanceMinTokens(params map[string]any, resolution string, hasVideoInput, fast bool) float64 {
	if !hasVideoInput {
		return 0
	}
	key := "min_video_input_tokens"
	if fast {
		key = "fast_min_video_input_tokens"
	}
	if normalizeResolution(resolution) == "1080p" {
		key += "_1080p"
	}
	return math.Max(0, paramFloat(params, key, 0))
}
