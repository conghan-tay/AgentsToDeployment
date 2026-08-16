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
| `agent` | repo, `services/agent/Dockerfile` | 8000 | Private |
| `mcp-tools` | repo, `services/mcp-tools/Dockerfile` | 8001 | Private |
| `Postgres` | Railway managed | 5432 | Private |
| `Redis` | Railway managed | 6379 | Private |
| `chroma` | image `chromadb/chroma:1.5.9` + volume `/data` | 8000 | Private, optional |

Services reach each other over Railway's private network at `<service>.railway.internal`.
Environments created after 2025-10-16 resolve those names to both IPv4 and IPv6, so the existing
`0.0.0.0` binds in all three Dockerfiles work unmodified. Older environments are IPv6-only and
would require binding `::` instead.

## Plan sizing

Railway's free plan allows **five services**. The full topology needs six. Decide before you start:

- **Hobby plan** — keep Chroma with a persistent volume and the full RAG path.
- **Free plan** — omit Chroma entirely and set `KNOWLEDGE_BACKEND=memory` on the agent. Retrieval
  degrades to the lexical `MemoryKnowledgeRepository`, and the knowledge base is held in the agent
  process, so it must be re-seeded after every restart or deploy.

Do not create Chroma and delete it later. Deleting a service orphans its volume, which keeps
billing until you remove it separately with `railway volume delete`.

## Prerequisites

1. Fork the repository. A fork is a new repository and does **not** inherit Railway's GitHub App
   grant — authorize it explicitly under **New → GitHub Repo → Configure GitHub App**.
2. Install and authenticate the CLI. Managed databases and volumes are CLI-only; they are not
   exposed over the Railway MCP server.

```bash
railway login
railway init --name support-agent-fork    # creates and links the project
railway status --json
```

## 1. Generate production secrets

Both services reject the demo credentials once `ENVIRONMENT=production`, in
`Settings.reject_demo_production_configuration` (`services/agent/app/core/settings.py`) and
`config.FromEnvironment` (`services/gateway/internal/config/config.go`).

```bash
openssl rand -hex 32 > api_key.txt            # public edge key
openssl rand -hex 32 > internal_api_key.txt   # gateway -> agent
```

`INTERNAL_API_KEY` must hold the same value on `gateway` and `agent` or every proxied request
returns 401.

## 2. Provision stateful services first

The agent's `lifespan` (`services/agent/app/main.py`) opens the PostgreSQL checkpointer during
startup and has no fallback, so the database must exist before the agent's first boot. Only MCP
failure is tolerated, in `ToolRegistry.create`.

```bash
railway add --database postgres --json
railway add --database redis --json
```

Always pass `--json`. Without it a successful create writes nothing to stdout, and a blind retry
silently provisions a second database.

Confirm the generated connection variables, because `${{Service.VAR}}` references are
case-sensitive:

```bash
railway variable list --service Postgres --json    # DATABASE_URL
railway variable list --service Redis --json       # REDIS_URL
```

Optional Chroma service, on a plan with room for it:

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
railway add --service agent   --repo YOURUSER/AgentsToDeployment --branch main --json
railway add --service gateway --repo YOURUSER/AgentsToDeployment --branch main --json
```

Each of these triggers an immediate deploy that **will fail**. See
[CLI behaviour worth knowing](#cli-behaviour-worth-knowing) below.

Then set build and deploy configuration per service:

| Service | `dockerfilePath` | `watchPatterns` | Healthcheck |
|---|---|---|---|
| `gateway` | `services/gateway/Dockerfile` | `services/gateway/**` | `/healthz` |
| `agent` | `services/agent/Dockerfile` | `services/agent/**`, `pyproject.toml`, `uv.lock` | `/healthz`, timeout 300 |
| `mcp-tools` | `services/mcp-tools/Dockerfile` | `services/mcp-tools/**`, `pyproject.toml`, `uv.lock` | none |

Leave `rootDirectory` unset. All three Dockerfiles `COPY pyproject.toml uv.lock ./` from the
repository root, so scoping the build root breaks them.

The agent's healthcheck needs a generous timeout: its image installs the full dependency set and
startup runs `AsyncPostgresSaver.setup()` plus MCP discovery before the app serves traffic.

Watch patterns matter here because all three services share one repository. Without them every
push rebuilds all three.

## 4. Set variables

`agent`:

```
ENVIRONMENT=production
PORT=8000
INTERNAL_API_KEY=<generated>
MODEL_PROVIDER=openai
MODEL_NAME=gpt-5-mini
OPENAI_API_KEY=<key>
LANGSMITH_TRACING=true
LANGSMITH_PROJECT=support-agent-railway
LANGSMITH_API_KEY=<key>
POSTGRES_DSN=${{Postgres.DATABASE_URL}}
MCP_SERVER_URL=http://mcp-tools.railway.internal:8001/mcp
CHECKPOINTER_BACKEND=postgres
KNOWLEDGE_BACKEND=memory
```

With a Chroma service, set `KNOWLEDGE_BACKEND=chroma` and add `CHROMA_HOST=chroma.railway.internal`,
`CHROMA_PORT=8000`, `CHROMA_SSL=false`, `CHROMA_COLLECTION=support_knowledge`.

`OPENAI_API_KEY` is required whenever `KNOWLEDGE_BACKEND=chroma`, independent of the chat provider:
`ChromaKnowledgeRepository.__init__` constructs `OpenAIEmbeddings` unconditionally.

`gateway`:

```
ENVIRONMENT=production
PORT=8080
GATEWAY_PORT=8080
API_KEY=<generated>
INTERNAL_API_KEY=<same value as agent>
AGENT_SERVICE_URL=http://agent.railway.internal:8000
REDIS_URL=${{Redis.REDIS_URL}}
```

Use literal `.railway.internal` hostnames rather than `${{mcp-tools.RAILWAY_PRIVATE_DOMAIN}}`. The
hyphen in the service name makes the reference syntax unreliable.

Pipe secrets through stdin so they never reach shell history:

```bash
tr -d '\n' < internal_api_key.txt | railway variable set INTERNAL_API_KEY --stdin --service agent
tr -d '\n' < internal_api_key.txt | railway variable set INTERNAL_API_KEY --stdin --service gateway
tr -d '\n' < api_key.txt          | railway variable set API_KEY --stdin --service gateway
```

## 5. Expose only the gateway

```bash
railway domain --service gateway --port 8080 --json
```

Never generate a domain for `agent`, `mcp-tools`, or `chroma`. The internal FastAPI routes are
guarded only by `X-Internal-API-Key`, and Chroma has no authentication at all.

## 6. Deploy and seed

Redeploy in dependency order so the agent finds MCP on its first boot:

```bash
railway redeploy --service mcp-tools --from-source --yes
railway redeploy --service agent     --from-source --yes
railway redeploy --service gateway   --from-source --yes
```

A queued build is not a deploy. Poll each service until it reaches a terminal state:

```bash
railway deployment list --service agent --environment production --limit 1 --json
```

Then seed the knowledge base through the public gateway. `scripts/seed_knowledge.py` prefers real
environment variables over the project `.env`, so no file needs editing:

```bash
API_BASE_URL=https://<your-domain> API_KEY=<generated> uv run python scripts/seed_knowledge.py
```

## Verification

```bash
curl -sS https://<your-domain>/healthz                       # 200
curl -sS -o /dev/null -w '%{http_code}\n' \
  -X POST https://<your-domain>/v1/tickets \
  -H 'Content-Type: application/json' -d '{}'                # 401, auth enforced
```

Exercise the read-only path, then the durable approval path, using the `curl` examples in the
[README](../README.md). A refund should return `waiting_approval` with a `pending_action`, and the
decision endpoint should resume the same thread to `completed` — that resume is what proves the
PostgreSQL checkpointer is working.

Confirm MCP loaded rather than falling back to the local demo tool:

```bash
railway logs --service agent --lines 300 | grep -E 'mcp_tools_loaded|mcp_unavailable'
```

build logs
```bash
railway logs --service agent --environment production --build --lines 200   # build instead of runtime
```

The distinction is visible in responses too. The MCP server's `lookup_order` derives status from
the order id, so `order-123` returns `processing` with a 5 business day estimate; the local
fallback in `tools/registry.py` always returns `in_transit` with 3 days.

Finally, confirm nothing private is exposed:

```bash
railway domain list --service agent --json      # expect no domains
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

- Keep replicas at 1. Refund idempotency lives in the in-process `_completed_actions` dictionary in
  `ToolRegistry`, so a second replica can execute the same approved action twice. Moving that key
  into the destination business system is the prerequisite for scaling out.
- With `KNOWLEDGE_BACKEND=memory`, re-seed after every agent restart or deploy.
- If PostgreSQL rejects the connection with an SSL error, use
  `POSTGRES_DSN=${{Postgres.DATABASE_URL}}?sslmode=disable`. psycopg's default `prefer` mode
  normally negotiates without help.
- `.env` is gitignored and is not copied by any Dockerfile, so local secrets never enter the images.
- Rotate `API_KEY` and `INTERNAL_API_KEY` on a schedule, and replace the shared edge key with
  JWT/OAuth validation at the gateway before serving real customers.
