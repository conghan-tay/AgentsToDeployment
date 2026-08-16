# Production deployment guide

The application is container-first. The Go gateway, Python agent, and MCP tool service
are stateless and can run on Kubernetes, Cloud Run/ECS-style container platforms, or a
developer PaaS. Durable state lives in managed services.

For a worked end-to-end example on a single managed PaaS, see
[`railway.md`](railway.md), which deploys the whole stack from a forked repository.

## Recommended managed layout

| Concern | Google Cloud | AWS |
|---|---|---|
| Containers | Cloud Run or GKE Autopilot | ECS Fargate or EKS |
| PostgreSQL checkpoints | Cloud SQL for PostgreSQL | RDS for PostgreSQL |
| Distributed rate limit | Memorystore for Redis | ElastiCache for Redis |
| Vector store | Chroma Cloud or a persistent Chroma workload | Chroma Cloud or persistent ECS/EKS workload |
| Secrets | Secret Manager | Secrets Manager |
| Images | Artifact Registry | ECR |

For the most developer-friendly first deployment, use Cloud Run for the three app
containers, Cloud SQL, Memorystore, and Chroma Cloud. Keep the agent and MCP services
private; only the Go gateway should accept internet traffic. Kubernetes manifests are
included for teams that want a portable starting point.

## Kubernetes

1. Build and push the three images with immutable tags.
2. Copy `deploy/k8s/app.yaml`; replace image names and managed-service endpoints.
3. Replace the example Secret with External Secrets or your cloud secret manager.
4. Apply the manifest: `kubectl apply -f deploy/k8s/app.yaml`.
5. Put TLS and a managed WAF/API gateway in front of the `gateway` Service.
6. Run the knowledge seed job once, then execute the E2E suite against its public URL.

The manifest uses two replicas and non-root, read-only containers. Add autoscaling based
on request concurrency and LLM latency after observing real traffic. PostgreSQL and
Redis should use private networking, encryption, backups, and multi-zone availability.

## Release checklist

- Pin images by digest; never deploy `latest` beyond the sample manifest.
- Rotate API and model keys and verify secret-manager access.
- Restrict CORS and place OAuth/JWT validation at the gateway for real customers.
- Run unit, race, E2E, and dependency/security scans.
- Enable LangSmith tracing with a production project and sampling/redaction policy.
- Set database backups, restore drills, alerts, latency/error SLOs, and cost budgets.
- Load-test with the chosen model because model latency controls overall concurrency.
- Replace the demo refund action and order MCP data with idempotent production APIs.

