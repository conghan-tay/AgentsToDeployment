# Railway deployment guide

A concrete, reproducible path for running this project on [Railway](https://railway.com) from a
forked repository. [`deployment.md`](deployment.md) covers the portable Kubernetes and GCP/AWS
mapping; this guide covers a single managed PaaS end to end, including the CLI behaviour that will
otherwise cost you an afternoon.

## Service topology

Railway builds each service from the same repository root, selecting a different Dockerfile per
service. Only the Go gateway receives a public domain.

| Railway service | Source | Port | Exposure |
|---|---|---|---|
| `gateway` | repo, `services/gateway/Dockerfile` | 8080 | Public domain |
| `worker` | repo, `services/agent/Dockerfile` | none | Private, no port at all |
| `mcp-tools` | repo, `services/mcp-tools/Dockerfile` | 8001 | Private |
| `Redis` | Railway managed | 6379 | Private |
| `chroma` | image `chromadb/chroma:1.5.9` + volume `/data` | 8000 | Private |

Durable execution comes from **Temporal Cloud**, not from a Railway service. Running a Temporal
cluster on Railway is possible but is a poor use of a PaaS: it wants its own database, several
roles, and careful upgrades. Temporal Cloud's free tier is sufficient for this template.

There is no PostgreSQL service any more. The previous design used it for LangGraph checkpoints;
Temporal now owns that state, and the application has no relational database.

Services reach each other over Railway's private network at `<service>.railway.internal`.
Environments created after 2025-10-16 resolve those names to both IPv4 and IPv6, so the existing
`0.0.0.0` binds in the gateway and MCP Dockerfiles work unmodified. Older environments are
IPv6-only and would require binding `::` instead.

## Plan sizing

Railway's free plan allows **five services**. This topology needs four: `gateway`, `worker`,
`mcp-tools`, and `chroma`, plus managed Redis. Chroma is now required — knowledge ingestion writes
to it from the gateway and retrieval reads from it in the worker, so there is no in-memory
fallback that both processes can share.

Do not create Chroma and delete it later. Deleting a service orphans its volume, which keeps
billing until you remove it separately with `railway volume delete`.

## Prerequisites

1. Fork the repository. A fork is a new repository and does **not** inherit Railway's GitHub App
   grant — authorize it explicitly under **New → GitHub Repo → Configure GitHub App**.
2. Install and authenticate the CLI. Managed databases and volumes are CLI-only; they are not
   exposed over the Railway MCP server.
3. Create a Temporal Cloud namespace and an API key.

```bash
railway login
railway init --name support-agent-fork    # creates and links the project
railway status --json
```

## 1. Generate production secrets

The gateway rejects the demo credential once `ENVIRONMENT=production`, in
`config.FromEnvironment` (`services/gateway/internal/config/config.go`). The worker rejects the
fake model in production, in `Settings.reject_demo_production_configuration`
(`services/agent/app/core/settings.py`).

```bash
openssl rand -hex 32 > api_key.txt            # public edge key
```

Only one shared secret now. The old `INTERNAL_API_KEY` is gone: the gateway reaches the agent over
Temporal's authenticated connection rather than over a private HTTP route, and the worker exposes
no port to guard.

## 2. Provision stateful services first

```bash
railway add --database redis --json
```

Always pass `--json`. Without it a successful create writes nothing to stdout, and a blind retry
silently provisions a second database.

Confirm the generated connection variables, because `${{Service.VAR}}` references are
case-sensitive:

```bash
railway variable list --service Redis --json       # REDIS_URL
```

Chroma:

```bash
railway add --service chroma --image chromadb/chroma:1.5.9 \
  --variables "IS_PERSISTENT=TRUE" --variables "PERSIST_DIRECTORY=/data" \
  --variables "ANONYMIZED_TELEMETRY=FALSE" --json
railway service link chroma
railway volume add --mount-path /data --json
```

## 3. Create the application services

```bash
railway add --service mcp-tools --repo YOURUSER/AgentsToDeployment --branch main \
  --variables "MCP_PORT=8001" --variables "PORT=8001" --json
railway add --service worker  --repo YOURUSER/AgentsToDeployment --branch main --json
railway add --service gateway --repo YOURUSER/AgentsToDeployment --branch main --json
```

Each of these triggers an immediate deploy that **will fail**. See
[CLI behaviour worth knowing](#cli-behaviour-worth-knowing) below.

Then set build and deploy configuration per service:

| Service | `dockerfilePath` | `watchPatterns` | Healthcheck |
|---|---|---|---|
| `gateway` | `services/gateway/Dockerfile` | `services/gateway/**` | `/readyz` |
| `worker` | `services/agent/Dockerfile` | `services/agent/**`, `pyproject.toml`, `uv.lock` | none |
| `mcp-tools` | `services/mcp-tools/Dockerfile` | `services/mcp-tools/**`, `pyproject.toml`, `uv.lock` | none |

The `worker` service must have **no healthcheck and no port**. Railway's healthchecks are HTTP
probes, and the worker serves no HTTP; configuring one guarantees a failed deploy. Railway will
report the service healthy as long as the process stays up. To check it properly, use the
container-level command the Compose and Kubernetes setups use:

```bash
railway run --service worker python -m app.healthcheck
```

Leave `rootDirectory` unset. All Dockerfiles `COPY pyproject.toml uv.lock ./` from the repository
root, so scoping the build root breaks them.

Watch patterns matter here because all three services share one repository. Without them every
push rebuilds all three.

## 4. Set variables

`worker`:

```
ENVIRONMENT=production
MODEL_PROVIDER=openai
MODEL_NAME=gpt-5-mini
OPENAI_API_KEY=<key>
LANGSMITH_TRACING=true
LANGSMITH_PROJECT=support-agent-railway
LANGSMITH_API_KEY=<key>
TEMPORAL_ADDRESS=<namespace>.<account>.tmprl.cloud:7233
TEMPORAL_NAMESPACE=<namespace>.<account>
TEMPORAL_API_KEY=<temporal cloud api key>
TEMPORAL_TLS=true
TEMPORAL_TASK_QUEUE=support-agent
CHROMA_HOST=chroma.railway.internal
CHROMA_PORT=8000
CHROMA_SSL=false
CHROMA_COLLECTION=support_knowledge
EMBEDDING_MODEL=text-embedding-3-small
MCP_SERVER_URL=http://mcp-tools.railway.internal:8001/mcp
```

`gateway`:

```
ENVIRONMENT=production
PORT=8080
GATEWAY_PORT=8080
API_KEY=<generated>
REDIS_URL=${{Redis.REDIS_URL}}
TEMPORAL_ADDRESS=<namespace>.<account>.tmprl.cloud:7233
TEMPORAL_NAMESPACE=<namespace>.<account>
TEMPORAL_API_KEY=<temporal cloud api key>
TEMPORAL_TLS=true
TEMPORAL_TASK_QUEUE=support-agent
APPROVAL_TIMEOUT_HOURS=72
CHROMA_URL=http://chroma.railway.internal:8000
CHROMA_COLLECTION=support_knowledge
EMBEDDING_MODEL=text-embedding-3-small
OPENAI_API_KEY=<key>
```

Three variables must be **identical** on both services or the system misbehaves quietly:
`TEMPORAL_TASK_QUEUE` (or the gateway starts work nothing will pick up), `CHROMA_COLLECTION`, and
`EMBEDDING_MODEL` (or the gateway writes vectors the worker cannot meaningfully search).

`OPENAI_API_KEY` is required on the **gateway**, not just the worker: ingestion embeds documents at
write time, independent of the chat provider.

Use literal `.railway.internal` hostnames rather than `${{mcp-tools.RAILWAY_PRIVATE_DOMAIN}}`. The
hyphen in the service name makes the reference syntax unreliable.

Pipe secrets through stdin so they never reach shell history:

```bash
tr -d '\n' < api_key.txt | railway variable set API_KEY --stdin --service gateway
```

## 5. Expose only the gateway

```bash
railway domain --service gateway --port 8080 --json
```

Never generate a domain for `worker`, `mcp-tools`, or `chroma`. Chroma has no authentication at
all, and the worker has no HTTP surface worth exposing.

## 6. Deploy and seed

Redeploy in dependency order so the worker finds MCP on its first boot:

```bash
railway redeploy --service mcp-tools --from-source --yes
railway redeploy --service worker    --from-source --yes
railway redeploy --service gateway   --from-source --yes
```

A queued build is not a deploy. Poll each service until it reaches a terminal state:

```bash
railway deployment list --service worker --environment production --limit 1 --json
```

Then seed the knowledge base through the public gateway. `scripts/seed_knowledge.py` prefers real
environment variables over the project `.env`, so no file needs editing:

```bash
API_BASE_URL=https://<your-domain> API_KEY=<generated> uv run python scripts/seed_knowledge.py
```

Seeding now exercises the gateway's own embed-and-upsert path; it never touches the worker.

## Verification

```bash
curl -sS https://<your-domain>/healthz                       # 200, liveness only
curl -sS https://<your-domain>/readyz                        # 200 once Temporal is reachable
curl -sS -o /dev/null -w '%{http_code}\n' \
  -X POST https://<your-domain>/v1/tickets \
  -H 'Content-Type: application/json' -d '{}'                # 401, auth enforced
```

Exercise the read-only path, then the durable approval path, using the `curl` examples in the
[README](../README.md). Remember that runs are asynchronous: `POST /v1/tickets` returns `202` with
`status: "running"`, and you poll `GET /v1/tickets/{id}` until it reports `waiting_approval`, then
`completed`.

The proof that durable execution works is a worker restart mid-approval:

```bash
# with a ticket parked at waiting_approval
railway redeploy --service worker --yes
# then approve it; the run resumes on the new container
```

Watch the same run in the Temporal Cloud UI. A completed refund's history shows one activity per
graph node, the `submit_decision` signal, and `support.apply_action` scheduled only *after* that
signal — which is the visible proof that the side effect cannot precede approval.

Confirm MCP loaded rather than falling back to the local demo tool:

```bash
railway logs --service worker --lines 300 | grep -E 'mcp_tools_loaded|mcp_unavailable'
```

build logs
```bash
railway logs --service worker --environment production --build --lines 200
```

The distinction is visible in responses too. The MCP server's `lookup_order` derives status from
the order id, so `order-123` returns `processing` with a 5 business day estimate; the local
fallback in `tools/registry.py` always returns `in_transit` with 3 days.

Finally, confirm nothing private is exposed:

```bash
railway domain list --service worker --json     # expect no domains
railway domain list --service mcp-tools --json  # expect no domains
```

## CLI behaviour worth knowing

- **The first deploy of every repo-backed service fails, by design of the ordering.**
  `railway add --repo` deploys immediately, before `dockerfilePath` can be set, so Railpack runs
  instead of Docker, detects Python, finds no start command, and errors. This is expected; set the
  configuration, then `railway redeploy --from-source`. Do not debug the first failure.
- **`railway environment edit --service-config` can silently no-op.** Setting `build.builder`
  through it exited 0 and changed nothing. Use the Railway MCP server or the dashboard. Setting
  `dockerfilePath` switches the builder to `DOCKERFILE` on its own, so `builder` never needs to be
  set explicitly.
- **`railway volume add` has no `--service` flag.** Supplying one at the parent level
  (`railway volume --service chroma add ...`) panics the CLI. Run `railway service link <service>`
  first, then `railway volume add --mount-path /data`.
- **`railway variable delete` rejects `--yes` and `--skip-deploys`**, which `railway variable set`
  accepts. Passing them makes the command do nothing without an obvious error.
- **Read configuration back after every mutation.** Several operations above exit 0 without
  applying. `railway environment config --json` and `railway variable list --json` are the source
  of truth, not command exit codes.

## Notes and caveats

- Keep `worker` replicas at 1. Refund idempotency lives in the in-process `_completed_actions`
  dictionary in `ToolRegistry`, so a second replica can execute the same approved action twice.
  Moving that key into the destination business system is the prerequisite for scaling out.
- `GET /v1/tickets/{id}` is a Temporal query and is answered by a worker. If every worker is down
  the gateway returns `502` even though the run itself is safe — a redeploy of `worker` briefly
  makes reads unavailable while writes stay durable.
- Set a retention period on the Temporal namespace. Closed histories are the audit trail for who
  approved which refund.
- `.env` is gitignored and is not copied by any Dockerfile, so local secrets never enter the images.
- Rotate `API_KEY` and the Temporal API key on a schedule, and replace the shared edge key with
  JWT/OAuth validation at the gateway before serving real customers.
