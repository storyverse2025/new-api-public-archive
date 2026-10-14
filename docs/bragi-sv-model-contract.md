# Bragi SV Model Contract

This document defines the contract between Bragi Canvas and this gateway for
SV-owned model names. It intentionally keeps Bragi unaware of upstream
providers, channels, and provider-specific model IDs.

Fallback-chain selection is not finalized in this document. The current contract
only defines stable model names and the first set of channel mappings.

## Goals

- Bragi sends one stable SV model name for a requested capability.
- The gateway may bind that SV model name to one or more channels.
- Each selected channel maps the SV model to its own upstream model via
  `channel.model_mapping`.
- Bragi does not choose `fal`, `apimart`, `byteplus`, `tokenrouter`, or any
  other upstream provider directly when using the SV NewAPI provider.
- Capability differences are represented in the SV model name for now, because
  current channel selection is model-based and not mode-aware.

## Non-Goals

- This document does not define backwards-compatible legacy aliases.
- This document does not define the final fallback-chain algorithm.
- This document does not add a `mode` or capability dimension to the gateway's
  channel selector.
- This document does not require every Bragi model to be available through SV
  NewAPI immediately.

## Naming Rules

### Model Names

Default naming:

```text
sv-{bragiModelId}
```

Capability-specific naming:

```text
sv-{bragiModelId}-{capability}
```

Use a capability-specific model name when channels that serve the Bragi model do
not expose equivalent capabilities. For example:

- `sv-kling-3.0` for regular Kling 3.0 text-to-video/image-to-video/first-last-frame.
- `sv-kling-3.0-motion-control` for Kling 3.0 motion control.
- `sv-seedance-2.0` and `sv-seedance-2.0-fast` as separate quality/speed tiers.

The type segment is intentionally omitted. Use `sv-gpt-image-2`, not
`sv-image-gpt-image-2`.

### Channel Names

Channel names should describe the upstream provider, account, region, or routing
domain. They should not describe a Bragi model, an SV model, or a single
capability.

Default provider-level naming:

```text
{provider}
```

Use a routing/account suffix only when it maps to a real operational boundary:

```text
{provider}-{routingDomain}
{provider}-{accountOrRegion}
```

Good examples:

- `tokenrouter`
- `byteplus`
- `apimart`
- `fal`
- `byteplus-cn` if a separate BytePlus region/account is used
- `fal-primary` if a separate FAL account is used

Avoid names such as `byteplus-seedream-lite`, `byteplus-seedance-2`,
`tokenrouter-gpt55`, or `apimart-images` unless the suffix is a real operational
boundary. Model and capability differences belong in `models` and
`model_mapping`, not in the channel name.

## Channel Selection Contract

For a Bragi request:

1. Bragi sends a canonical SV model name.
2. The gateway selects a channel from `abilities` by group and model.
3. The selected channel maps the SV model to its upstream model using
   `model_mapping`.
4. The selected channel adaptor converts the request into the provider-specific
   endpoint and request shape.

Current gateway channel selection does not inspect Bragi mode, input media
shape, or provider-specific capability metadata. Therefore, every channel bound
to the same SV model must be capability-equivalent for Bragi's intended usage of
that model.

## Bragi Direct Provider Background

This section records how Bragi Canvas currently handles non-SV-NewAPI providers.
It is background for this contract, not a target gateway API. Bragi's direct
providers are more expressive than today's gateway selector because they can
combine:

- A model catalog entry with `supportedProviders[provider].apiModelId`.
- Optional provider-level mode restrictions with
  `supportedProviders[provider].modes`.
- Optional `aggregated: true`, where the catalog ID is an umbrella and the
  provider implementation chooses the real upstream model internally.
- Optional `refDelivery` overrides, such as native provider assets for BytePlus
  Seedance.
- Provider code that inspects `genMode`, input media shape, and provider params
  to choose endpoints and request payloads.

Relevant direct-provider routing observed in Bragi:

| Bragi provider | Relevant Bragi models | Upstream endpoint/routing behavior |
| --- | --- | --- |
| `kling` | `kling-3.0`, `kling-2.6` | Uses `https://api.klingai.com`; `text-to-video` routes to `/v1/videos/text2video`, `first-frame` and `first-last-frame` route to `/v1/videos/image2video`, and `motion-control` routes to `/v1/videos/motion-control`. |
| `fal` | Kling, Seedance, Grok, Veo, image/audio models | Images and audio submit to `https://fal.run/{apiModelId}`. Video submits to `https://queue.fal.run/{modelId}` and rewrites the model suffix by mode, such as `/text-to-video`, `/image-to-video`, `/reference-to-video`, or `/extend-video`. |
| `apimart` | `gpt-image-2`, `nano-banana-*`, `omni-flash-ext`, Kling motion control | Images submit to `/v1/images/generations` and poll `/v1/tasks/{task_id}`. Video submits to `/v1/videos/generations`. Kling motion control uses the same video submit endpoint but a different schema with singular `image_url` and `video_url`. |
| `bytedance` / `byteplus` Seedream | `seedream-*` image models | Submits directly to the configured Ark image generation endpoint, defaulting to `/api/v3/images/generations`. BytePlus uses the AP Southeast Ark base URL when configured as `byteplus`. |
| `bytedance` / `byteplus` Seedance | `seedance-2.0`, `seedance-2.0-fast` | Submits directly to the configured Ark content-generation task endpoint, defaulting to `/api/v3/contents/generations/tasks`, and polls the task URL. BytePlus/TokenRouter variants can use provider-native `asset://` refs. |
| `gemini` / `veo` | `veo-3.1`, `veo-3.1-lite` | Uses `models/{modelId}:predictLongRunning`. The endpoint stays stable, but `genMode` changes the instance payload: `image`, `lastFrame`, or `referenceImages`. |
| `tokenrouter` | GPT image, Seedance, HappyHorse, text models | Uses OpenAI-compatible base `https://api.tokenrouter.com/v1`. Video submits to `/video/generations`; Seedance, HappyHorse, and generic video models use different payload fields for refs. |
| `xai` | Grok image/video/audio | Images choose `/v1/images/generations` or `/v1/images/edits`. Video chooses `/v1/videos/generations` or `/v1/videos/extensions` by `genMode`; `first-frame` and `image-ref` change payload shape. |
| `dashscope` | `wan-2.7`, CosyVoice/Qwen audio | Wan 2.7 is `aggregated: true`: Bragi shows umbrella `wan-2.7`, while the provider maps modes to upstream IDs such as `wan2.7-t2v-2026-04-25`, `wan2.7-i2v-2026-04-25`, `wan2.7-r2v`, and `wan2.7-videoedit` before submitting to DashScope video synthesis tasks. |

Implication for SV NewAPI: when Bragi calls the gateway, it should not rely on a
single `apiModelId` plus `genMode` to express every provider-specific branch
until the gateway selector becomes capability-aware. For the first phase,
capability differences that require different endpoint or payload behavior
should be represented as different SV model names.

## Storyverse Client Model Inventory

Storyverse monorepo is another expected client for this gateway. This section is
only an inventory of model/provider usage relevant to SV NewAPI mapping. It does
not document Storyverse product policy, surface-specific behavior, or
cross-model fallback. If a whole model becomes unavailable, choosing a different
model remains a client-owned decision.

| Storyverse model | Provider fallback order for the same model/capability | Current SV model |
| --- | --- | --- |
| `openai/gpt-5.5` | TokenRouter | `sv-gpt-5.5` |
| `gpt-image-2` | APIMart -> FAL/OpenAI-compatible -> TokenRouter/OpenAI-compatible | `sv-gpt-image-2` |
| `gpt-image-2-official` | APIMart (OpenAI official channel; honors `quality`) | `sv-gpt-image-2-official` |
| `gpt-image-2.5-flare` | APIMart | `sv-gpt-image-2.5-flare` |
| `gpt-image-2.5-sunburst` | APIMart | `sv-gpt-image-2.5-sunburst` |
| `reactor/fast-h3-omni` | Reactor official SDK worker | `sv-fast-h3-omni` |
| `nanobanana-pro` | APIMart | `sv-nano-banana-pro` |
| `nanobanana` | FAL | `sv-nano-banana-2` when added |
| `seedream-5` | BytePlus or FAL -> APIMart | `sv-seedream-5.0-lite` |
| `grok-imagine-image-quality` | xAI | Future mapping if delegated to NewAPI |
| `luma-uni-1` | Luma | Future mapping if delegated to NewAPI |
| `seedance_byteplus` / `seedance-2.0` | BytePlus -> FAL | `sv-seedance-2.0` |
| `kling3` | Kling native -> FAL | `sv-kling-3.0` when native Kling is added; FAL is seeded temporarily |
| `sora2` | FAL | `sv-sora-2` |
| ElevenLabs music | ElevenLabs -> FAL | `sv-elevenlabs-music` when added |

## First-Batch Canonical Models

These models are the first contract surface to implement. Legacy names should not
be used by Bragi for new SV NewAPI calls. The default channel column describes
the currently seeded preferred provider where NewAPI already has the matching
adaptor. Unsupported providers are called out below instead of being faked
through a different channel.

| Client model | Canonical SV model | Default NewAPI channel | Upstream mapping | Status |
| --- | --- | --- | --- | --- |
| `gpt-5.5` / Storyverse `openai/gpt-5.5` | `sv-gpt-5.5` | `tokenrouter` | `openai/gpt-5.5` | Seeded |
| Storyverse / Bragi `gpt-image-2` | `sv-gpt-image-2` | `apimart` | `gpt-image-2` | Seeded |
| Storyverse / Bragi `gpt-image-2-official` | `sv-gpt-image-2-official` | `apimart` | `gpt-image-2-official` | Seeded; forwards `quality` |
| GPT Image 2.5 Flare | `sv-gpt-image-2.5-flare` | `apimart` | `gpt-image-2.5-flare` | Seeded; actual upstream USD cost settlement |
| GPT Image 2.5 Sunburst | `sv-gpt-image-2.5-sunburst` | `apimart` | `gpt-image-2.5-sunburst` | Seeded; actual upstream USD cost settlement |
| Reactor Fast H3 Omni | `sv-fast-h3-omni` | `reactor` | `reactor/fast-h3-omni` | Optional official SDK worker; GPU-session-second settlement |
| Storyverse / Bragi `nanobanana-pro` | `sv-nano-banana-pro` | `apimart` | `gemini-3-pro-image-preview` | Seeded |
| Storyverse / Bragi Seedream image generation | `sv-seedream-5.0-lite` | `byteplus` | `${SEEDREAM_LITE_ENDPOINT_ID}` | Seeded for image generations |
| Storyverse / Bragi Seedream image generation fallback | `sv-seedream-5.0-lite` | `apimart` | `doubao-seedream-5-0-lite` | Seeded as lower-priority same-model candidate |
| Storyverse `seedance_byteplus` / Bragi `seedance-2.0` | `sv-seedance-2.0` | `byteplus` | `${SEEDANCE_20_ENDPOINT_ID}` | Seeded |
| Storyverse / Bragi `seedance-2.0` FAL fallback | `sv-seedance-2.0` | `fal` | `fal-ai/seedance-2/reference-to-video` | Seeded as lower-priority same-model candidate |
| Storyverse `sora2` | `sv-sora-2` | `fal` | `fal-ai/sora-2/image-to-video` | Seeded |
| Storyverse / Bragi `grok-video` | `sv-grok-video` | `fal` | `xai/grok-imagine-video` | Seeded |
| Bragi `veo-3.1` | `sv-veo-3.1` | `fal` | `fal-ai/veo3.1` | Seeded |
| Bragi `elevenlabs-tts-v3` | `sv-elevenlabs-tts-v3` | `fal` | `fal-ai/elevenlabs/tts/eleven-v3` | Seeded |
| Bragi `minimax-tts` | `sv-minimax-tts` | `fal` | `fal-ai/minimax/speech-2.8-hd` | Seeded |
| Bragi `elevenlabs-sfx` | `sv-elevenlabs-sfx` | `fal` | `fal-ai/elevenlabs/sound-effects/v2` | Seeded |
| Storyverse `seedream-5` FAL text/edit path | `sv-seedream-5.0-lite` | `fal` image endpoint | `fal-ai/bytedance/seedream/v5/lite/{text-to-image,edit}` | Needs FAL image adaptor for text/edit provider fallback |
| Storyverse `nanobanana` | `sv-nano-banana-2` | `fal` image endpoint | `fal-ai/nano-banana-2` / `fal-ai/nano-banana-2/edit` | Needs FAL image adaptor |
| Storyverse `kling3` native | `sv-kling-3.0` | Kling native OmniVideo | `kling-v3-omni` | Needs Kling OmniVideo adaptor |
| ElevenLabs music | `sv-elevenlabs-music` | ElevenLabs native music | ElevenLabs music SDK/API | Needs native ElevenLabs music channel |

Temporary seed note: `sv-kling-3.0` is currently mapped to FAL
`fal-ai/kling-video/v3/pro` because NewAPI does not yet have a native Kling
OmniVideo adaptor. When native Kling is added, it should become the preferred
channel and the FAL path should become a fallback-layer channel.

### `sv-gpt-image-2-official` Billing (quality × resolution)

Unlike `sv-gpt-image-2` (flat per-image `fixed_image` rule), `sv-gpt-image-2-official`
is billed with the `image_quality_resolution` rule (`pkg/billingcalc/rules.go`),
because APIMart prices it per a quality × resolution matrix. Prices are seeded in
`deploy/seed_billing_rules.sh` under `channel_name:apimart:sv-gpt-image-2-official`:

| quality \ resolution | 1k | 2k | 4k |
| --- | --- | --- | --- |
| low | $0.00304 | $0.00392 | $0.00904 |
| medium | $0.026 | $0.03408 | $0.08024 |
| high | $0.1036 | $0.13576 | $0.32032 |

Final cost = matched cell price × `n`. Two contract points Bragi must be aware of:

1. **`quality` defaults to `medium` for billing.** APIMart's own default quality is
   `auto`, which is **not** a priced tier. Any `auto`, absent, or unrecognized
   `quality` value is billed at `default_quality` (currently `medium`). To be billed
   at a specific tier, Bragi must send an explicit `quality` of `low`/`medium`/`high`.
   (This is the override-able `default_quality` rule param — raise it to `high` if the
   product prefers conservative billing for `auto`.)
2. **Send an explicit `resolution` (`1k`/`2k`/`4k`).** The rule reads `resolution`
   first; if absent it derives the tier from a pixel `size` (e.g. `2048x1152`) by total
   pixel count (`≤2.3M → 1k`, `≤5M → 2k`, else `4k`), and an aspect-ratio-only `size`
   like `16:9` carries no pixel info so it falls back to `default_resolution`
   (currently `1k`). Sending `resolution` explicitly avoids any derivation ambiguity.

## Channel Migration Plan

The current seed scripts use model-scoped channel names. The target design should
collapse those into provider-scoped channel names where credentials, base URL,
settings, priority, and quota boundaries are shared.

| Current seed channel | Target channel | Migration action |
| --- | --- | --- |
| `tokenrouter-gpt55` | `tokenrouter` | Rename or recreate as provider-level TokenRouter channel, then attach `sv-gpt-5.5`. |
| `apimart-images` | `apimart` | Rename once APIMart is treated as a provider-level channel. The current adaptor can remain image-only during the first batch. |
| `byteplus-seedream-lite` | `byteplus` | Merge into provider-level BytePlus channel if it uses the same account, base URL, and quota pool as Seedance. |
| `byteplus-seedance-2` | `byteplus` | Merge into provider-level BytePlus channel and keep video asset settings on that channel. |
| `fal-media` | `fal` | Rename to provider-level FAL channel unless a separate media-only routing boundary is intentionally needed. |

The seed script now performs an API-level upsert: if a target provider-scoped
channel exists, it updates it; otherwise it looks for the legacy model-scoped
channel name and updates that row to the target name. This rebuilds abilities via
the existing channel update path.

Current seed priority convention:

| Seeded channel | Priority | Weight | Meaning |
| --- | --- | --- | --- |
| `tokenrouter` | 100 | 100 | Default text channel for `sv-gpt-5.5`. |
| `apimart` | 100 | 100 | Default image channel for `sv-gpt-image-2`, `sv-gpt-image-2-official`, and `sv-nano-banana-pro`. |
| `byteplus` | 110 | 100 | Preferred channel for `sv-seedream-5.0-lite` image generation and `sv-seedance-2.0` video. |
| `fal` | 100 | 100 | Default channel for currently seeded FAL video/audio models. |

Because ability priority is channel-wide today, do not add fallback-only mappings
for a model to a provider channel that also owns default mappings for other
models unless the priority implications are acceptable. The current
`sv-seedream-5.0-lite` and `sv-seedance-2.0` candidates use `byteplus` priority
110 and lower-priority provider candidates at 100 as a coarse approximation.
The detailed fallback chain work should introduce per-model policy before we
attach broader non-default fallback candidates such as TokenRouter
`openai/gpt-image-2` to `sv-gpt-image-2`.

## Bragi Mapping Contract

Bragi's SV NewAPI provider should map the first batch as follows. Rows marked
`needs adaptor` should not be enabled in Bragi until the corresponding NewAPI
adaptor exists or the product accepts the temporary channel noted above.

| Bragi model | SV NewAPI `apiModelId` | Seed status |
| --- | --- | --- |
| `gpt-5.5` | `sv-gpt-5.5` | Seeded |
| `gpt-image-2` | `sv-gpt-image-2` | Seeded |
| `gpt-image-2-official` | `sv-gpt-image-2-official` | Seeded |
| `nano-banana-pro` | `sv-nano-banana-pro` | Seeded |
| `seedream-5.0-lite` | `sv-seedream-5.0-lite` | Seeded to BytePlus for image generations; FAL image adaptor still needed for text/edit provider fallback |
| `seedance-2.0` | `sv-seedance-2.0` | Seeded |
| `kling-3.0` regular video | `sv-kling-3.0` | Seeded to FAL temporarily; native Kling needs adaptor |
| `grok-video` | `sv-grok-video` | Seeded |
| `veo-3.1` | `sv-veo-3.1` | Seeded |
| `elevenlabs-tts-v3` | `sv-elevenlabs-tts-v3` | Seeded |
| `minimax-tts` | `sv-minimax-tts` | Seeded |
| `elevenlabs-sfx` | `sv-elevenlabs-sfx` | Seeded |

## Planned Follow-Up Models

These are not part of the first implementation batch, but should follow the
same naming contract when added.

| Bragi model or capability | Future SV model |
| --- | --- |
| `gpt-5.5-pro` | `sv-gpt-5.5-pro` |
| `nano-banana-2` | `sv-nano-banana-2` |
| `seedance-2.0-fast` | `sv-seedance-2.0-fast` |
| `kling-3.0` motion control | `sv-kling-3.0-motion-control` |
| `kling-2.6` | `sv-kling-2.6` |
| `veo-3.1-lite` | `sv-veo-3.1-lite` |
| `omni-flash-ext` | `sv-omni-flash-ext` |
| `elevenlabs-music` | `sv-elevenlabs-music` |
| `minimax-music` | `sv-minimax-music` |
| `wan-2.7` text-to-video | `sv-wan-2.7-t2v` |
| `wan-2.7` image-to-video | `sv-wan-2.7-i2v` |
| `wan-2.7` reference-to-video | `sv-wan-2.7-r2v` |
| `wan-2.7` video edit | `sv-wan-2.7-video-edit` |

## Fallback Chain Initial Direction

The current gateway already supports multiple channels for the same model via
`abilities`, with `priority` and `weight` used for selection. The exact fallback
chain semantics still need a separate design.

Gateway fallback should only order providers/channels that serve the same
canonical SV model and capability. Cross-model fallback is intentionally outside
this contract; if an entire model is unavailable, the client decides whether to
switch to a different model.

Initial direction:

- Define provider fallback order per canonical SV model, not globally per
  provider and not across different models.
- Keep the first phase compatible with current `abilities.priority` and
  `abilities.weight`: priority orders fallback layers; weight load-balances
  equivalent channels inside the same layer.
- Track attempted channel IDs during one request so retry/fallback does not pick
  the same failed channel again.
- Use deterministic layer order. Weighted random is acceptable only within a
  layer whose channels are capability-equivalent.
- Require each fallback layer to declare the errors that may advance the chain.
  Retryable/transient errors can fall back; hard rejections must stop.
- Hard-stop errors should include content safety, copyright, likeness/privacy,
  invalid input, unsupported parameters, auth, quota, billing, and account
  configuration failures.
- Operational errors can fall back: timeout, 408/429 where policy allows it,
  5xx, network failures, provider unavailable, provider no-output, and temporary
  runner/scheduling failures.
- Record fallback attempts in request/job metadata with provider/channel,
  upstream model, status, error class, duration, and whether the error was
  considered fallback-eligible.
- Add per-layer time budgets. A slow primary should not consume the whole
  request budget if later layers are expected to run.
- Consider circuit breakers or short-lived channel health suppression for known
  transient provider outages.
- Preserve client-owned cross-model fallback. The gateway should only own
  provider fallback for the exact canonical model requested by the client.

Practical first-phase shape:

```text
sv_model
  layer 0:
    channels: [provider-primary-a, provider-primary-b]
    selection: weighted
    fallback_on: timeout, rate_limit, provider_unavailable, provider_no_output, network, 5xx
    stop_on: safety, copyright, likeness, invalid_input, auth, quota, billing
    timeout_budget_seconds: 600
  layer 1:
    channels: [provider-fallback-a]
    selection: deterministic_or_weighted
    fallback_on: timeout, provider_unavailable, provider_no_output, network, 5xx
    stop_on: safety, copyright, likeness, invalid_input, auth, quota, billing
    timeout_budget_seconds: 600
```

This keeps the public client contract centered on `sv_model`, while letting the
gateway understand that two channels offering the same model are not always
equally valid fallback targets.

Current NewAPI capability fit:

| Capability | Current status |
| --- | --- |
| Multiple channels for one model | Supported through channel `models` and per-channel `model_mapping`. |
| Layered channel preference | Partially supported through model ability `priority`; higher-priority channels are selected before lower-priority channels. |
| Load balancing among equivalent channels | Supported through model ability `weight` inside one priority layer. |
| Basic retry/fallback loop | Supported by the relay retry loop, bounded by `RetryTimes`. |
| Retry stop hints | Partially supported through `skip_retry` errors and status-code retry settings. |
| Channel health suppression | Partially supported through existing channel error processing and auto-disable logic. |
| Attempt visibility | Partially supported through request error logs and `use_channel` tracking. |

Gaps for the full contract:

| Required capability | Current gap |
| --- | --- |
| Per-model fallback policy | No declarative `fallback_on` / `stop_on` policy exists per SV model yet. |
| Provider-specific error taxonomy | Current retry behavior is mostly status-code and `skip_retry` based; it does not reliably classify safety, copyright, likeness, invalid input, quota, billing, timeout, runner failure, and provider outage across providers. |
| Exhaust same-layer channels first | Current selection is priority-index based. A retry can move to the next priority layer before all channels in the current layer have been tried. If only one priority layer exists, retry may randomly pick the same channel again. |
| Exclude already-attempted channels | `use_channel` is recorded, but channel selection does not currently take attempted channel IDs as an exclusion set. |
| Structured fallback chain metadata | There is no first-class `fallback_chain` record with channel ID, provider, mapped upstream model, reason, status, latency, and outcome for each attempt. |
| Per-layer timeout budgets | No model-layer-specific timeout budget exists. |
| Capability-aware channel matching | Selector is model-based; it does not yet match by mode, input media shape, endpoint family, or capability constraints. |
| Async post-submit fallback | Task submit retries can fall through to another channel, but terminal failures after a task is accepted are not represented as a cross-provider fallback chain. |

Therefore the immediate implementation can use current `priority` / `weight`
as a coarse provider fallback approximation, but the full contract needs
selector and error-classification work before it is safe to let arbitrary
same-model channels participate in fallback.

## Detailed Fallback Design Roadmap

A later detailed version should split fallback into four explicit layers:

1. Policy: what fallback chain is allowed for one canonical SV model.
2. Selection: which channel is selected next for the current request.
3. Classification: whether the previous error is eligible for fallback.
4. Observability: what happened across all attempts.

### Policy model

Fallback policy should be defined per canonical SV model, with optional
capability constraints.

```text
sv_model: sv-gpt-image-2
capability:
  endpoint: image_generation
  input: text_or_image
  output: image
layers:
  - name: primary
    channels: [apimart-us, apimart-sg]
    selection: weighted
    fallback_on: [timeout, rate_limit, provider_unavailable, network, 5xx]
    stop_on: [safety, copyright, likeness, invalid_input, unsupported_params, auth, quota, billing]
    timeout_budget_seconds: 60
  - name: fallback
    channels: [fal-openai-image]
    selection: deterministic
    fallback_on: [timeout, provider_unavailable, network, 5xx]
    stop_on: [safety, copyright, likeness, invalid_input, unsupported_params, auth, quota, billing]
    timeout_budget_seconds: 60
```

Policy storage options:

| Option | Tradeoff |
| --- | --- |
| Seed-managed JSON config first | Fastest to ship, easy to review in deploy scripts, but not admin-editable. |
| DB table such as `model_fallback_policies` | Better long-term control, but needs migrations, admin UI, validation, and audit history. |
| Extend model metadata | Simple if policy remains small, but can overload model metadata with routing behavior. |

Recommended path: start with seed-managed JSON or static config for the first
SV batch, then migrate to a DB-backed policy once the schema stabilizes.

### Selector changes

The selector should accept request-scoped exclusion and capability context:

```text
SelectChannel(model, group, capability, attempted_channel_ids, policy_state)
```

Required behavior:

- Never select a channel already attempted in the same fallback chain.
- Exhaust eligible channels in the current layer before moving to the next
  layer, unless policy explicitly says to advance immediately.
- Use weighted selection only inside a layer of capability-equivalent channels.
- Treat `priority` and `weight` as compatibility inputs during migration, but
  let the fallback policy become the source of truth for chain ordering.
- Preserve current group and auto-group behavior, but make cross-group fallback
  explicit in policy where possible.

### Error classification

Provider adapters should normalize errors into a small gateway taxonomy:

| Error class | Fallback? | Notes |
| --- | --- | --- |
| `timeout` | yes | Includes request timeout and provider task timeout. |
| `rate_limit` | maybe | Fallback only if policy allows it for that model/provider. |
| `provider_unavailable` | yes | 5xx, outage, temporary unavailable, network failure. |
| `provider_no_output` | yes | Empty output or invalid provider response after accepted request. |
| `runner_failure` | yes | Temporary queue/task runner failure. |
| `safety` | no | Must not be bypassed by switching provider. |
| `copyright` | no | Must stop. |
| `likeness` | no | Must stop. |
| `invalid_input` | no | Client should fix input. |
| `unsupported_params` | no | Model/provider mismatch; should be caught before request where possible. |
| `auth` | no | Channel configuration problem; do not charge through fallback blindly. |
| `quota` | no by default | Usually a channel/account configuration or budget issue; clients can decide whether cross-model fallback is acceptable. |
| `billing` | no | Must stop. |

Implementation should keep current `skip_retry` and status-code retry behavior
as compatibility signals, but provider adapters should gradually return typed
error classes.

### Attempt metadata

Each attempt should write structured fallback metadata:

```text
fallback_chain:
  - attempt: 1
    layer: primary
    channel_id: 12
    channel_name: apimart-us
    channel_type: apimart
    sv_model: sv-gpt-image-2
    upstream_model: gpt-image-2
    status_code: 503
    error_class: provider_unavailable
    fallback_eligible: true
    latency_ms: 1842
  - attempt: 2
    layer: fallback
    channel_id: 19
    channel_name: fal
    channel_type: fal
    upstream_model: openai/gpt-image-2
    status_code: 200
    outcome: success
    latency_ms: 9231
```

This should be visible to admin logs first. Client-facing exposure can be added
later as a debug/admin-only option.

### Task model behavior

For async video/audio/image tasks, fallback needs two stages:

| Stage | Fallback behavior |
| --- | --- |
| Submit failure | Can fall back immediately if error class is eligible. |
| Accepted task terminal failure | Should only fall back if the provider failure is clearly operational and the policy allows a second task submission. Safety or invalid-input terminal failures must stop. |

Task fallback should record task IDs from every provider attempt so operators can
debug duplicate submissions, billing risk, and provider-side late completions.

### Rollout plan

| Phase | Scope |
| --- | --- |
| 1. Coarse fallback | Use current `priority` / `weight` / `RetryTimes`; bind only capability-equivalent channels to the same canonical SV model. |
| 2. Selector hygiene | Add attempted-channel exclusion and same-layer exhaustion. Keep existing abilities as the source of ordering. |
| 3. Typed error classes | Normalize provider errors into the gateway taxonomy and use them in `shouldRetry`. |
| 4. Policy config | Add seed-managed per-model fallback policy with `fallback_on`, `stop_on`, layer ordering, and timeout budget. |
| 5. Observability | Persist structured fallback chain metadata in logs/task metadata. |
| 6. DB/admin support | Move policy to DB/admin UI after the schema proves stable. |
| 7. Client migration | Let clients delegate provider fallback only for models whose same-model provider chain is represented in the gateway. |

Open questions:

- Should a retry exclude channels already attempted during the same request?
- Should the selector exhaust all channels at one priority before moving to the
  next priority?
- Should fallback be deterministic per request, weighted-random, or a hybrid?
- Should channel health, recent failures, latency, or cost influence ordering?
- Should future ability matching include mode or input media shape?

Until that design is finalized, every channel bound to the same canonical SV
model must be treated as capability-equivalent.
