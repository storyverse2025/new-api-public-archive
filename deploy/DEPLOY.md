# SV Gateway — Production Deployment Runbook (EC2 + Supabase)

This document covers standing up the gateway on an AWS EC2 instance with Supabase as the database and Caddy for automatic HTTPS.

---

## 1. Supabase Setup

### Main database

1. Create a new Supabase project at [supabase.com](https://supabase.com).
2. In **Project Settings → Database → Connection String**, select the **Session pooler** tab (port **5432**, IPv4-compatible).
3. Copy the connection string and paste it into `SQL_DSN` in `.env.prod`. Always append `?sslmode=require`:
   ```
   SQL_DSN=postgresql://postgres.<ref>:<password>@<region>.pooler.supabase.com:5432/postgres?sslmode=require
   ```
   - Use the **Session pooler** (port 5432), NOT the transaction pooler (port 6543). GORM uses prepared statements which are incompatible with the transaction pooler.
4. new-api **auto-migrates** all tables on first boot — no manual SQL required.

> **Free tier note:** Supabase free tier provides 500 MB. The `request_logs` table (Plan H) stores full prompt/response bodies and can grow quickly under production load.

### Log database (recommended)

Create a second Supabase project (or any separate Postgres instance) dedicated to heavy request/usage logs. Copy its Session-pooler connection string into `LOG_SQL_DSN`. This keeps analytics writes off the main OLTP database.

If `LOG_SQL_DSN` is left empty, logs fall back to the main database.

---

## 2. EC2 Setup

1. **Launch an EC2 instance** (Amazon Linux 2023 or Ubuntu 22.04 recommended, t3.small or larger).

2. **Install Docker + Compose plugin:**
   ```bash
   # Amazon Linux 2023
   sudo dnf install -y docker
   sudo systemctl enable --now docker
   sudo usermod -aG docker ec2-user
   # Log out and back in, then:
   sudo mkdir -p /usr/local/lib/docker/cli-plugins
   sudo curl -SL https://github.com/docker/compose/releases/latest/download/docker-compose-linux-x86_64 \
     -o /usr/local/lib/docker/cli-plugins/docker-compose
   sudo chmod +x /usr/local/lib/docker/cli-plugins/docker-compose

   # Ubuntu
   # sudo apt-get update && sudo apt-get install -y docker.io docker-compose-plugin
   ```

3. **Security group rules:** Open inbound ports **80** (HTTP) and **443** (HTTPS) only.
   - Do NOT expose port 3000 (gateway), 5432 (DB), or 6379 (Redis) to the internet.

4. **DNS:** Point an A record for `GATEWAY_DOMAIN` (e.g. `gateway.storyverseai.art`) to the EC2 instance's public IP. Caddy will use this for automatic TLS certificate provisioning via ACME (Let's Encrypt).

---

## 3. Configure

Clone the repository and check out the `sv/deploy` branch:
```bash
git clone https://github.com/your-org/new-api.git && cd new-api
git checkout sv/deploy
```

Copy the example env file and fill in all values:
```bash
cp deploy/.env.prod.example deploy/.env.prod
```

Generate secure random secrets:
```bash
openssl rand -hex 32   # paste into SESSION_SECRET
openssl rand -hex 32   # paste into CRYPTO_SECRET
openssl rand -hex 32   # paste into REDIS_PASSWORD (also update REDIS_CONN_STRING)
```

Paste the Supabase connection strings into `SQL_DSN` and (recommended) `LOG_SQL_DSN`.

Fill in all upstream API keys and endpoint IDs in the `UPSTREAM KEYS` section. These are consumed by `seed_channels.sh` and are not needed by the running container.

For fal support, set `FAL_KEY` (or export `FAL_API_KEY` before running the seed script). The seed script uses this key to create the `fal` channel after the updated gateway image is deployed.

---

## 4. Boot

This deployment supports two TLS/proxy modes. **Production runs in host-caddy mode.** Choose the matching command below and use the *same* invocation for every subsequent `up`, rebuild, or single-service recreate.

### Host-caddy mode (production)

Caddy runs as a **host process** (systemd, config at `/etc/caddy/Caddyfile`) that reverse-proxies `gateway.<domain>` → `127.0.0.1:3000`. The `docker-compose.host-caddy.yml` override publishes the gateway port to `127.0.0.1:3000` so the host caddy can reach it.

Bring up (or rebuild) **only the `sv-gateway` service** — redis comes up as its dependency:

```bash
docker compose -f deploy/docker-compose.prod.yml -f deploy/docker-compose.host-caddy.yml \
  --env-file deploy/.env.prod up -d --build sv-gateway
```

Three details are all mandatory — each has bitten a real redeploy:

> ⚠️ **Both `-f` files.** Omitting `docker-compose.host-caddy.yml` recreates the container **without** the `127.0.0.1:3000:3000` publish → host caddy can't reach it → every request **502** (even though the gateway is healthy). Verify: `docker port sv-gateway` must print `3000/tcp -> 127.0.0.1:3000`.
>
> ⚠️ **`--env-file deploy/.env.prod`.** The redis service's `--requirepass ${REDIS_PASSWORD}` is resolved by Compose **variable interpolation**, which only reads `--env-file` (not the service's `env_file:`). Omit it and redis starts with an **empty** password while the gateway still authenticates with one → `[FATAL] Redis ping test failed: ERR AUTH ... without any password configured` → crash-loop.
>
> ⚠️ **Target `sv-gateway`, not bare `up -d`.** The override does not disable the in-container `caddy` service, so a bare `up -d` also starts it and it collides with the host caddy on port 80 (`address already in use`). Naming the service avoids starting caddy.

### All-in-docker mode (self-contained alternative)

An in-container caddy terminates TLS and proxies to sv-gateway over the docker network (port 3000 is only `expose`d). Use this **only if you are not running a host caddy**:

```bash
docker compose -f deploy/docker-compose.prod.yml --env-file deploy/.env.prod up -d --build
```

### Check health

```bash
docker compose -f deploy/docker-compose.prod.yml -f deploy/docker-compose.host-caddy.yml ps
docker compose -f deploy/docker-compose.prod.yml logs sv-gateway --tail=50
```

> **Startup window:** on every boot the gateway runs GORM AutoMigrate against the (remote) database *before* it binds `:3000`. Against Supabase this can take several minutes, during which the host caddy returns **502**. This is expected — confirm readiness from inside the container before testing the public URL:
> ```bash
> docker exec sv-gateway wget -qO- http://localhost:3000/api/status
> ```

Caddy provisions/serves a TLS certificate for `GATEWAY_DOMAIN`. This requires that the DNS A record is already pointing at the EC2 IP and that ports 80 and 443 are open.

---

## 5. Bootstrap Admin + Seed Channels

### First boot: create the root admin

On first startup, navigate to `https://GATEWAY_DOMAIN` in a browser and complete the initial setup (create root user). Alternatively, call the setup API:
```bash
curl -X POST https://GATEWAY_DOMAIN/api/setup \
  -H "Content-Type: application/json" \
  -d '{"username":"root","password":"<your-root-password>","display_name":"Root"}'
```

Add the root credentials to `deploy/.env.prod`:
```
GATEWAY_ROOT_PASSWORD=<your-root-password>
# OR generate/retrieve an access token from the admin panel and use:
GATEWAY_ROOT_ACCESS_TOKEN=<token>
```

### Run the seed script

```bash
bash deploy/seed_channels.sh
```

This script is idempotent — it is safe to run multiple times. It will:
- Set `theme.frontend=default` so production uses the newer default dashboard. New SV admin surfaces such as **Models → Routing** are maintained in the default UI first; the classic UI is legacy-compatible and may not expose these pages.
- Set `SelfUseModeEnabled=true` (required for model routing to work without per-model pricing)
- Register groups `sv-monorepo` and `bragi-canvas` with ratio 1
- Create access tokens `sv-monorepo-token` and `bragi-canvas-token` (unlimited, no expiry)
- Upsert the provider-scoped upstream channels (`tokenrouter`, `byteplus`, `apimart`, `fal`)
- Migrate legacy channel names to the provider-scoped names when found

If you skip the seed script, set the frontend theme manually before using the SV admin dashboard:
```bash
curl -X PUT https://GATEWAY_DOMAIN/api/option/ \
  -H "Authorization: Bearer <root-access-token>" \
  -H "Content-Type: application/json" \
  -d '{"key":"theme.frontend","value":"default"}'
```

After deploying a gateway image that adds a new provider or model mapping, run `bash deploy/seed_channels.sh` again so the production database matches the current seed definition. The script is idempotent: existing tokens are skipped by name, system options are re-applied with the same values, and provider-scoped channels are updated in place.

### APIMart GPT Image 2.5 key and models

Create an upstream key at [APIMart API Keys](https://apimart.ai/keys). Both
GPT Image 2.5 variants use the same key. Set `APIMART_API_KEY` in the gitignored
`deploy/.env.prod`; this variable is used by the seed script to store the key in
the channel database. Setting it only on the gateway container does not update
an existing channel. Alternatively, edit the **apimart** channel in the admin
console: type **APIMart (58)**, base URL `https://api.apimart.ai`, and paste the
upstream key into its **Key** field (without the `Bearer ` prefix).

The seeded model mappings are:

| Gateway model | APIMart model |
| --- | --- |
| `sv-gpt-image-2.5-flare` | `gpt-image-2.5-flare` |
| `sv-gpt-image-2.5-sunburst` | `gpt-image-2.5-sunburst` |

For a manual channel update, add the two gateway names to **Models** and the
mapping below to **Model Redirect**, preserving the existing entries:

```json
{
  "sv-gpt-image-2.5-flare": "gpt-image-2.5-flare",
  "sv-gpt-image-2.5-sunburst": "gpt-image-2.5-sunburst"
}
```

Deploy the updated gateway binary before running both seed scripts. Run them
with `GATEWAY_URL` pointing to the intended gateway and root credentials set in
`deploy/.env.prod`. The full channel seed also requires the other provider keys
listed in its validation step; manual channel editing avoids that requirement.
Clients authenticate to svrouter using a **gateway token**, not the APIMart key.

See the [APIMart GPT Image 2.5 API reference](https://docs.apimart.ai/en/api-reference/images/gpt-image-2.5/generation)
for parameter limits. The adaptor forwards `size`, `resolution`, `quality`,
`image_urls`, `mask_url`, `background`, `moderation`, `output_format`, and
`output_compression`. An explicit compression of `0` is preserved. Use JSON
`image_urls` and `mask_url` for editing; local uploads/multipart edit conversion
are not implemented by this APIMart adaptor.

### Run the billing seed script

```bash
set -a; source deploy/.env.prod; set +a   # provides GATEWAY_URL + root creds
bash deploy/seed_billing_rules.sh
```

This script writes `billing_setting.billing_rule` and `billing_setting.billing_rule_params` for the seeded `sv-*` media models. Re-running it is safe: it replaces the two billing option values with the current script output. Price constants can be overridden with environment variables such as `FAL_VEO_*`, `FAL_ELEVENLABS_PRICE_PER_1K_CHARACTERS`, or by passing `BILLING_RULES_FILE=deploy/my-billing-rules.json`.

GPT Image 2.5 uses the `upstream_cost` rule for both canonical and raw model
names. It reserves `$1.00 × n` by default, then settles against APIMart's actual
`data.cost` in USD, with excess quota refunded. This value already includes all
generated images and token categories; `credits_cost` is not a USD amount.
Override the reservation with `APIMART_GPT_IMAGE25_RESERVE_PER_IMAGE` and any
markup with `APIMART_GPT_IMAGE25_COST_MULTIPLIER` (default `1.00`). The reservation
is configurable and is not a guaranteed cap on upstream usage. Both synchronous
`/v1/images/generations` and asynchronous `/v1/images/tasks` use actual-cost
settlement. If a completed asynchronous response lacks a valid USD cost, the
gateway logs the settlement error and retains the reservation for reconciliation.

### Reactor Fast H3 Omni worker

Fast H3 Omni uses Reactor's session/WebRTC protocol. The gateway adapts it to
the existing asynchronous video API through an internal worker using the pinned
official Python SDK and saves the received video/audio frames as MP4. See [Reactor's model API](https://www.reactor.inc/models/fast-h3-omni/api).

Set these values in the gitignored `deploy/.env.prod`:

```dotenv
REACTOR_API_KEY=<your-reactor-key>
REACTOR_WORKER_SECRET=<independent-random-secret>
REACTOR_WORKER_URL=http://reactor-worker:8091
REACTOR_SESSION_MAX_SECONDS=120
REACTOR_GPU_PRICE_PER_SECOND=0.0125
```

Generate the worker secret with `openssl rand -hex 32`. The upstream API key
stays in the worker; the **reactor** channel (type **60**) stores the worker
secret. Client applications store only their gateway URL and gateway token.
The worker's HTTP port is internal to the compose network, and both task and
MP4 endpoints require its secret. Its database and outputs use the persistent
`reactor_worker_data` volume.

```bash
docker compose --env-file deploy/.env.prod \
  -f deploy/docker-compose.prod.yml -f deploy/docker-compose.reactor.yml \
  up -d --build
bash deploy/seed_channels.sh
bash deploy/seed_billing_rules.sh
```

The optional channel is seeded only when `REACTOR_WORKER_SECRET` is set. It maps
`sv-fast-h3-omni` to `reactor/fast-h3-omni`. The `reactor_gpu_seconds` billing rule
reserves the configured session cap (default 120 seconds), then settles the
worker's measured time from GPU readiness to session disconnect at the configured
GPU-second rate. Output clip duration is a separate value and does not determine
the final fee. Keep the worker cap and billing seed setting aligned. The default
rate is `$0.0125` per GPU second; adjust it if the upstream account pricing changes.

Submit through `/v1/videos`, poll `/v1/videos/<id>`, then download the result at
`/v1/videos/<id>/content`, using the same gateway token throughout:

```json
{
  "model": "sv-fast-h3-omni",
  "prompt": "A blue cube rotating slowly on a white table",
  "metadata": {
    "seconds": 5.875,
    "canvas": "1:1",
    "seed": 0
  }
}
```

Allowed clip durations are `5.875`, `8`, `10.125`, `12.25`, and `14.375` seconds;
use `metadata.seconds` to preserve fractions. Supported canvases are `16:9`,
`1:1`, `9:16`, and `4:3`, with dimensions fixed by the model. Use
`metadata.reference_images` (up to nine public image URLs) and
`metadata.reference_audios` (up to three public audio URLs). First/last frame
selections require at least one appearance reference image. Each task has its own bounded session;
cross-session continuation and arbitrary resolutions are not supported.

Failed or interrupted tasks follow the gateway's existing refund policy. Reactor
can still charge GPU usage incurred by a failed job. The worker marks unfinished
jobs as failed after restart; it cannot recover the exact meter for a crashed
session. Successful outputs remain available in the worker volume.

To **add models to an existing channel** outside the full seed path (e.g. new fal video models on the already-seeded `fal` channel), patch it in place:
```bash
set -a; source deploy/.env.prod; set +a   # provides GATEWAY_URL + root creds
bash deploy/update_fal_channel.sh           # idempotently merges new models + mappings
```
Or edit the channel's **Models** and **Model Redirect (mapping)** fields manually in the admin console. Either way, wait ~30s for the channel cache to sync (see below).

> **Note:** After channel creation, the in-memory channel cache syncs every 30 seconds (`CHANNEL_UPDATE_FREQUENCY`). Wait ~30s before testing.

---

## 6. Verify

Retrieve a token key from the admin panel (`https://GATEWAY_DOMAIN` → Tokens → copy key), then:
```bash
# Text generation
curl -s https://GATEWAY_DOMAIN/v1/chat/completions \
  -H "Authorization: Bearer sk-..." \
  -H "Content-Type: application/json" \
  -d '{"model":"sv-gpt-5.5","messages":[{"role":"user","content":"ping"}]}'

# Image generation
curl -s https://GATEWAY_DOMAIN/v1/images/generations \
  -H "Authorization: Bearer sk-..." \
  -H "Content-Type: application/json" \
  -d '{"model":"sv-gpt-image-2","prompt":"a red apple","n":1,"size":"1024x1024"}'

# GPT Image 2.5 (change flare to sunburst for the second variant)
curl -s https://GATEWAY_DOMAIN/v1/images/generations \
  -H "Authorization: Bearer <gateway-token>" \
  -H "Content-Type: application/json" \
  -d '{"model":"sv-gpt-image-2.5-flare","prompt":"a red apple","n":1,"size":"1:1","resolution":"1k","quality":"medium"}'

# Submit an asynchronous image task; poll /v1/images/tasks/<task_id> with
# the same gateway token until status becomes completed or failed.
curl -s https://GATEWAY_DOMAIN/v1/images/tasks \
  -H "Authorization: Bearer <gateway-token>" \
  -H "Content-Type: application/json" \
  -d '{"model":"sv-gpt-image-2.5-sunburst","prompt":"a red apple","n":1,"size":"1:1","resolution":"1k","quality":"medium"}'
```

---

## 7. Backups

- **Main DB (Supabase):** Supabase manages automated daily backups. Point-in-time recovery is available on paid plans.
- **Log DB (self-hosted Postgres):** Add a `pg_dump` cron job on the log DB host, e.g.:
  ```bash
  # /etc/cron.d/sv-log-backup
  0 3 * * * postgres pg_dump -Fc sv_logs > /backups/sv-logs-$(date +%F).dump
  ```
- **Redis:** Redis is used for caching only. Data loss on restart is acceptable; the gateway will resync from the DB.

---

## 8. Gotchas

| Issue | Detail |
|-------|--------|
| **Session pooler vs transaction pooler** | Always use port 5432 (Session pooler). GORM's prepared-statement cache is incompatible with the transaction pooler (port 6543). |
| **`sslmode=require` in DSN** | Supabase requires TLS. Omitting this will result in connection refused. |
| **Never change `CRYPTO_SECRET`** | This key encrypts channel API keys stored in the DB. Changing it after channels exist will make all stored keys unreadable, breaking all upstream calls. |
| **`SelfUseModeEnabled`** | Currently enabled (`true`) which bypasses per-model billing checks. This is intentional for internal use. If you later enable multi-product billing and user quotas, disable this and configure model prices. |
| **Channel cache lag** | After `seed_channels.sh` creates channels, the in-memory cache takes up to 30s to sync. Test after waiting. |
| **502 after rebuild (host-caddy mode)** | Production proxies via a host caddy → `127.0.0.1:3000`. Recreating sv-gateway **without** `-f deploy/docker-compose.host-caddy.yml` drops the `127.0.0.1:3000:3000` publish, so caddy can't reach it → 502. Always pass both `-f` files; verify with `docker port sv-gateway`. See §4. |
| **502 during startup** | On every boot the gateway runs AutoMigrate before binding `:3000`; against Supabase this takes several minutes and caddy returns 502 until it finishes. Check `docker exec sv-gateway wget -qO- http://localhost:3000/api/status`. |
| **Redis AUTH crash-loop** | `[FATAL] Redis ping test failed: ERR AUTH ... without any password configured` means redis was started without `--env-file deploy/.env.prod`, so `${REDIS_PASSWORD}` interpolated empty while the gateway still authenticates. Re-run the boot command WITH `--env-file`. See §4. |
| **BytePlus base URL** | Use `https://ark.ap-southeast.bytepluses.com` (overseas). The Beijing URL `https://ark.cn-beijing.volces.com` returns 401 for overseas accounts. |
| **Caddy TLS provisioning** | DNS A record must be live before first boot. Caddy will fail to get a cert if the domain doesn't resolve to the server's public IP. |
| **Free-tier Supabase pause** | Supabase free-tier projects pause after 7 days of inactivity. Upgrade to a paid plan or use the Pro tier for production. |
