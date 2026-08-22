# Production deployment guide

The application is container-first. The Go gateway, Python worker, and MCP tool service
are stateless and can run on Kubernetes, Cloud Run/ECS-style container platforms, or a
developer PaaS. Durable state lives in Temporal and in the vector store.

For a worked end-to-end example on a single managed PaaS, see
[`railway.md`](railway.md), which deploys the whole stack from a forked repository.

## Recommended managed layout

| Concern | Google Cloud | AWS |
|---|---|---|
| Containers | Cloud Run or GKE Autopilot | ECS Fargate or EKS |
| Durable execution | Temporal Cloud (or self-hosted on GKE) | Temporal Cloud (or self-hosted on EKS) |
| Distributed rate limit | Memorystore for Redis | ElastiCache for Redis |
| Vector store | Chroma Cloud or a persistent Chroma workload | Chroma Cloud or persistent ECS/EKS workload |
| Secrets | Secret Manager | Secrets Manager |
| Images | Artifact Registry | ECR |

For the most developer-friendly first deployment, use Cloud Run for the three app
containers, Temporal Cloud, Memorystore, and Chroma Cloud. Keep the worker and MCP
services private; only the Go gateway should accept internet traffic.

Note that the application has **no relational database**. Workflow history is Temporal's
responsibility, which is the main operational difference from a checkpointer-backed
design: back up and retain Temporal, not a Postgres instance you own.

## Temporal

Either option works; pick before you size anything else.

- **Temporal Cloud** — set `TEMPORAL_ADDRESS` to your namespace endpoint,
  `TEMPORAL_NAMESPACE` to `<namespace>.<account>`, `TEMPORAL_API_KEY` to an API key, and
  `TEMPORAL_TLS=true`. Both the gateway and the worker need these. This is the
  lowest-operations path and is what the Kubernetes manifest assumes.
- **Self-hosted** — run a Temporal cluster with its own PostgreSQL or Cassandra
  persistence. The `temporalio/temporal server start-dev` container in `compose.yaml` is
  a development convenience only: it stores everything in a single SQLite file and is
  not a production topology.

Set a retention period on the namespace. Closed workflow histories are your audit trail
for who approved which refund, so retention is a compliance decision, not a storage one.

## Scaling

The gateway scales on request concurrency, as any HTTP service does. The worker does
not: it polls a task queue and exposes no port, so scale it on **task-queue backlog**
(`temporal_workflow_task_schedule_to_start_latency`) and on LLM latency. Two independent
signals, two independent autoscaling policies.

Keep worker replicas at 1 until refund idempotency moves out of the in-process
`_completed_actions` dictionary in `ToolRegistry` — see the note in the README.

## The knowledge contract

Ingestion runs in the gateway (`internal/knowledge`) and retrieval runs in the worker
(`app/knowledge/repository.py`). They agree only by configuration, so `CHROMA_COLLECTION`
and `EMBEDDING_MODEL` must be identical in both services. A mismatch does not raise an
error anywhere; it just returns poor results. Deploy them together and treat those two
variables as a single unit.

`OPENAI_API_KEY` is required on the gateway regardless of which chat provider the worker
uses, because embeddings are computed at write time.

## Kubernetes

1. Build and push the three images with immutable tags.
2. Copy `deploy/k8s/app.yaml`; replace image names, the Temporal endpoint, and the
   managed-service endpoints.
3. Replace the example Secret with External Secrets or your cloud secret manager.
4. Apply the manifest: `kubectl apply -f deploy/k8s/app.yaml`.
5. Put TLS and a managed WAF/API gateway in front of the `gateway` Service.
6. Run the knowledge seed job once, then execute the E2E suite against its public URL.

The manifest uses non-root, read-only containers. The `worker` Deployment has no `ports`
and no `Service`, and probes it with `python -m app.healthcheck`, which asserts that this
process is actually polling its task queue — a port check would prove nothing for a
worker. The gateway's readiness probe hits `/readyz`, which verifies Temporal
reachability; its liveness probe hits `/healthz`, which deliberately does not, so a
Temporal blip cannot cause a restart loop.

## Release checklist

- Pin images by digest; never deploy `latest` beyond the sample manifest.
- Rotate the API key and model keys; verify secret-manager access.
- Restrict CORS and place OAuth/JWT validation at the gateway for real customers.
- Run unit, race, E2E, and dependency/security scans.
- Confirm `EMBEDDING_MODEL` and `CHROMA_COLLECTION` match across gateway and worker.
- Enable LangSmith tracing with a production project and sampling/redaction policy.
- Set Temporal namespace retention, alerts on task-queue backlog, and latency/error SLOs.
- Load-test with the chosen model because model latency controls overall concurrency.
- Replace the demo refund action and order MCP data with idempotent production APIs.
- Decide the approval deadline (`APPROVAL_TIMEOUT_HOURS`) with the business; the default
  auto-rejects after 72 hours so no ticket stays open forever.
