# ScaleODM Helm Chart

Kubernetes-native auto-scaling and load balancing for OpenDroneMap.

## Prerequisites

- Kubernetes 1.19+
- Helm 3.0+
- Argo Workflows (can be installed via this chart or pre-installed)

## Architecture

ScaleODM deploys into its own namespace (the Helm release namespace). All resources
live in this namespace: the API server, workflow pods, secrets, and service accounts.

The Argo Workflows controller runs in a separate namespace (default: `argo`) and
watches **all namespaces** for Workflow CRDs. When ScaleODM creates a workflow in
the release namespace, the Argo controller picks it up automatically.

This means:
- **One runtime secret** in the release namespace serves the API server and workflow pods
- No cross-namespace secret replication needed
- RBAC is scoped to the release namespace

## Installation

### Quick Start (OCI registry)

The ScaleODM chart is published as an **OCI Helm chart** at:

- `oci://ghcr.io/hotosm/charts/scaleodm`

You do **not** need to add a classic HTTP Helm repo; you can install directly from the OCI registry:

```bash
# Install the chart from OCI
helm install scaleodm oci://ghcr.io/hotosm/charts/scaleodm \
  --namespace scaleodm --create-namespace \
  --version <chart-version> \
  --set database.external.enabled=true \
  --set s3.external.enabled=true \
  --set secrets.runtime.name="scaleodm-secrets" \
  --set argo.enabled=true
```

Replace `<chart-version>` with the desired chart version (e.g. the latest release).

### Local Chart Development Install

```bash
# Install from the local `./chart` directory
helm install scaleodm ./chart \
  --namespace scaleodm --create-namespace \
  --set database.external.enabled=true \
  --set s3.external.enabled=true \
  --set secrets.runtime.name="scaleodm-secrets" \
  --set argo.enabled=true
```

### Install with Argo Workflows Subchart

By default, Argo Workflows is installed as a subchart in the `argo` namespace:

```bash
helm install scaleodm ./chart \
  --namespace scaleodm --create-namespace \
  --set database.external.enabled=true \
  --set s3.external.enabled=true \
  --set secrets.runtime.name="scaleodm-secrets" \
  --set argo.enabled=true
```

### Install without Argo Workflows Subchart

If Argo Workflows is already installed in your cluster:

```bash
helm install scaleodm ./chart \
  --namespace scaleodm --create-namespace \
  --set database.external.enabled=true \
  --set s3.external.enabled=true \
  --set secrets.runtime.name="scaleodm-secrets" \
  --set argo.enabled=false
```

## Configuration

### Required Values

At least one database and one S3 configuration path must be provided:

- **Database** (choose one):
  - `database.external.enabled=true` with `secrets.runtime.name` pointing at a Secret key that contains the full PostgreSQL URI under `secrets.runtime.keys.databaseUrl` (default key `SCALEODM_DATABASE_URL`), or
  - `database.postgres.enabled=true` and corresponding `database.postgres.auth.*` for the bundled Postgres subchart.

- **S3** (same unified runtime secret in the release namespace):
  - `secrets.runtime.name` must point to a Secret containing:
    - `SCALEODM_DATABASE_URL`
    - `AWS_S3_ENDPOINT`
    - `AWS_ACCESS_KEY_ID`
    - `AWS_SECRET_ACCESS_KEY`
  - Include `AWS_DEFAULT_REGION` when your object store requires an explicit region. If omitted, runtime defaults to `us-east-1`.

The same runtime secret is referenced by both the API deployment and workflow pods (via `AWS_S3_SECRET_NAME`).

### Optional Values

See [values.yaml](values.yaml) for all available configuration options.

### Ingress Security Semantics (UI-only)

ScaleODM chart ingress is intentionally constrained for security until API authentication/authorization exists.

- Chart ingress exposes only `/ui` and `/ui/...` (implemented as `path: /ui`, `pathType: Prefix`)
- Root/API routes (for example `/`, `/task/*`, `/openapi.json`) are intentionally not exposed by chart ingress
- `ingress.enabled=true` requires `config.ui.enabled=true`; Helm rendering fails otherwise
- Do not use rewrite-target annotations with this ingress; the app already serves UI under `/ui`
- `api.service.type` defaults to `ClusterIP` and should remain internal-only

This guardrail applies only to ingress resources rendered by this chart. Operators can still expose API routes if they create separate Ingress/Gateway resources outside this chart.

### Endpoint Policy and Workflow Guardrails

ScaleODM now exposes optional endpoint policy controls and workflow runtime guardrails via environment-backed chart values:

- `config.s3EndpointPolicy.enforceAllowlist` / `config.s3EndpointPolicy.allowedEndpoints`
- `config.workflowMissingGraceSeconds`
- `config.server.*` for HTTP server read/header/write/idle timeout hardening
- `api.probes.*` for probe endpoint paths (defaults: `/__lbheartbeat__` liveness, `/__heartbeat__` readiness)
- `config.readiness.*` for `/__heartbeat__` dependency checks (DB + Argo/K8s, optional S3)
- `config.ui.*` for optional built-in `/ui` operator pages (disabled by default; read-only mode enabled by default)
- `/health` and `/ready` remain compatibility aliases for existing integrations
- `config.workflow.*` for active deadline, retry, TTL, pod GC behavior, and workspace storage mode (`auto|emptyDir|pvc`)
- `config.workflow.resources.*` for per-container requests/limits
- `config.processSizing.*` for image-count-based process resource estimation (memory interpolation table + headroom, CPU, ephemeral storage)
- `config.observability.*` for OpenTelemetry bootstrap (OTLP endpoint, sampling, traces/metrics toggles)

These settings are conservative by default and can be tuned per deployment environment.

### Observability backend strategy

ScaleODM instrumentation is OpenTelemetry-first and vendor-neutral. The application emits OTLP traces and metrics that can be routed to any compatible backend. For Sentry users, the recommended path is collector/exporter routing from OTLP into Sentry ingestion, rather than binding ScaleODM business logic directly to the Sentry Go SDK.

### Probe Endpoints (Dockerflow convention)

By default, probes use Dockerflow-style endpoints:

- `GET /__lbheartbeat__` - lightweight process heartbeat (liveness)
- `GET /__heartbeat__` - dependency-aware readiness (DB + Argo/K8s + optional S3)

Compatibility aliases are also exposed:

- `GET /health` - alias of `GET /__lbheartbeat__`
- `GET /ready` - alias of `GET /__heartbeat__`

You can override probe paths via:

- `api.probes.livenessPath`
- `api.probes.readinessPath`

### Workflow Workspace Storage Modes

Workflow workspace storage is configurable through `config.workflow.workspace.*`:

- `mode`: `auto` (default), `emptyDir`, or `pvc`
- `size`: workspace PVC size request when PVC mode is used (default `30Gi`)
- `storageClass`: optional storage class (for example `gp3`, Ceph class)
- `accessMode`: PVC access mode (default `ReadWriteOnce`)
- `dynamicSize.*`: opt-in dynamic PVC estimation knobs (`enabled`, `multiplier`, `minGiB`, `maxGiB`, `fallbackMBPerImage`)

Behavior matrix:

- `mode=emptyDir`: always use `emptyDir`
- `mode=pvc`: always request a PVC
- `mode=auto` + `storageClass` set: request a PVC
- `mode=auto` + empty `storageClass`: fall back to `emptyDir`

Dynamic workspace sizing precedence:

1. Workspace mode resolves whether PVC is used
2. If workspace is not PVC, dynamic sizing is ignored
3. If dynamic sizing is disabled, static `workspace.size` is used
4. If enabled, ScaleODM estimates from `image_total_bytes` first, then `image_count * fallbackMBPerImage` (default `20`)
5. Result is multiplied, clamped to `minGiB/maxGiB`, rounded up to whole `Gi`, and falls back to static size on invalid inputs

Recommended profiles:

- AWS production: `mode=pvc`, `storageClass=gp3`, tune `size` to workload
- On-prem production: `mode=pvc`, `storageClass=<ceph-class>`
- Local/dev: keep defaults (`auto` with empty storage class, using `emptyDir`)

Example: AWS / EKS production with `gp3`

```bash
helm upgrade --install scaleodm oci://ghcr.io/hotosm/charts/scaleodm \
  --namespace scaleodm --create-namespace \
  --version <chart-version> \
  --set database.external.enabled=true \
  --set s3.external.enabled=true \
  --set secrets.runtime.name="scaleodm-secrets" \
  --set config.workflow.workspace.mode="pvc" \
  --set config.workflow.workspace.storageClass="gp3" \
  --set config.workflow.workspace.size="200Gi" \
  --set config.workflow.workspace.accessMode="ReadWriteOnce"
```

This creates a per-workflow PVC using the `gp3` StorageClass, which is the recommended pattern for larger ODM jobs on AWS.

Example: lightweight test cluster with `emptyDir`

```bash
helm upgrade --install scaleodm oci://ghcr.io/hotosm/charts/scaleodm \
  --namespace scaleodm --create-namespace \
  --version <chart-version> \
  --set database.external.enabled=true \
  --set s3.external.enabled=true \
  --set secrets.runtime.name="scaleodm-secrets" \
  --set config.workflow.workspace.mode="emptyDir"
```

This keeps job workspace storage on the node's ephemeral disk and is best suited to local development, test clusters, or small/lightweight workloads.

### External runtime secret

For external PostgreSQL + external S3, create one Secret in the release namespace:

```bash
kubectl create secret generic scaleodm-secrets \
  -n scaleodm \
  --from-literal=SCALEODM_DATABASE_URL="postgres://user:password@host:5432/scaleodm?sslmode=require" \
  --from-literal=AWS_S3_ENDPOINT="s3.amazonaws.com" \
  --from-literal=AWS_ACCESS_KEY_ID="your-access-key" \
  --from-literal=AWS_SECRET_ACCESS_KEY="your-secret-key" \
  --from-literal=AWS_DEFAULT_REGION="us-east-1"
```

Then install:

```bash
--set database.external.enabled=true \
--set s3.external.enabled=true \
--set secrets.runtime.name="scaleodm-secrets"
```

`AWS_DEFAULT_REGION` is optional for runtime compatibility. If omitted, ScaleODM workflow/runtime defaults to `us-east-1`; set it explicitly when your S3-compatible backend requires a different region.

### Bundled Postgres

To deploy a Postgres instance via the bundled Bitnami subchart:

```bash
--set database.postgres.enabled=true \
--set database.postgres.auth.password="your-password"
```

### Bundled Garage

To deploy Garage in-cluster:

```bash
--set s3.external.enabled=false \
--set s3.garage.enabled=true \
--set s3.garage.admin.token="<garage-admin-token>" \
--set s3.garage.rpc.secret="<garage-rpc-secret>" \
--set s3.garage.auth.accessKey="<s3-access-key>" \
--set s3.garage.auth.secretKey="<s3-secret-key>"
```

## Argo Workflows Configuration

### Deploy Argo Workflows via Subchart

When `argo.enabled=true` (default), Argo Workflows is deployed as a subchart
in the `argo` namespace. The controller watches all namespaces for workflow CRDs:

```yaml
argo:
  enabled: true
  namespaceOverride: argo
  server:
    enabled: true
  controller:
    enabled: true
    # Parallelism limits concurrent workflows to prevent overwhelming worker nodes
    # Recommended: (number of worker nodes) * 2-3
    # Example: 3 worker nodes = 6-9 concurrent workflows
    parallelism: 10
```

### Parallelism Configuration

The `argo.controller.parallelism` setting limits the total number of workflows that can run concurrently across the entire cluster. This prevents overwhelming worker nodes with too many resource-intensive ODM processing jobs.

**Recommendations:**
- **Small clusters (1-2 worker nodes)**: 4-6 concurrent workflows
- **Medium clusters (3-5 worker nodes)**: 6-15 concurrent workflows
- **Large clusters (6+ worker nodes)**: 12-30+ concurrent workflows

**Formula:** `parallelism = (number of worker nodes) * 2-3`

This accounts for:
- Each ODM workflow can be CPU/memory intensive
- Multiple workflows per node allows better resource utilization
- Prevents node exhaustion while maintaining throughput

> [!NOTE]
> Set to `0` to disable the limit (not recommended for production as it can overwhelm nodes).

> [!NOTE]
> **Autoscaling and queueing behavior:** Cluster autoscalers such as [Karpenter](https://karpenter.sh/) can be used alongside this setting to add nodes when workflow pods are unschedulable. `argo.controller.parallelism` still acts as a hard cap on concurrently running workflows, while Kubernetes scheduling determines when pods can start. If there are not enough resources, workflows/pods remain pending and are processed as capacity becomes available (immediately on existing nodes, or after autoscaled nodes come online).

### Use Existing Argo Workflows

If Argo Workflows is already installed and its controller watches all namespaces:

```yaml
argo:
  enabled: false
```

**Important:** If using an existing Argo Workflows installation, you may need to manually configure parallelism by creating/updating the `workflow-controller-configmap` ConfigMap in the Argo namespace:

```bash
kubectl create configmap workflow-controller-configmap \
  --from-literal=parallelism=10 \
  -n argo
```

## Uninstallation

```bash
helm uninstall scaleodm -n scaleodm
```

## Troubleshooting

### Check Pod Status

```bash
kubectl get pods -n scaleodm -l app.kubernetes.io/name=scaleodm
```

### Check Logs

```bash
kubectl logs -n scaleodm -l app.kubernetes.io/name=scaleodm
```

### Check Argo Workflows

```bash
kubectl get pods -n argo
kubectl get workflows -n scaleodm
```

### Verify Database Connection

```bash
kubectl exec -n scaleodm -it deployment/scaleodm -- env | grep SCALEODM_DATABASE_URL
```

### Verify S3 Configuration

```bash
kubectl exec -n scaleodm -it deployment/scaleodm -- env | grep SCALEODM_S3
```

## Values Reference

| Parameter | Description | Default |
|-----------|-------------|---------|
| `api.image.repository` | Image repository | `ghcr.io/hotosm/scaleodm` |
| `api.image.tag` | Image tag | `""` (uses Chart.AppVersion) |
| `api.replicaCount` | Number of replicas | `1` |
| `api.service.type` | Service type | `ClusterIP` |
| `api.service.port` | Service port | `31100` |
| `api.probes.livenessPath` | Liveness probe path | `"/__lbheartbeat__"` |
| `api.probes.readinessPath` | Readiness probe path | `"/__heartbeat__"` |
| `database.external.enabled` | Use external PostgreSQL via existing Secret | `true` |
| `secrets.runtime.name` | Unified runtime Secret used by API + workflow pods | `"scaleodm-secrets"` |
| `secrets.runtime.keys.databaseUrl` | Key in runtime Secret for DB URI | `"SCALEODM_DATABASE_URL"` |
| `database.external.secret.name` | Deprecated alias for `secrets.runtime.name` | `""` |
| `database.external.secret.key` | Deprecated alias for `secrets.runtime.keys.databaseUrl` | `""` |
| `database.postgres.enabled` | Deploy bundled Postgres subchart | `false` |
| `s3.external.secret.name` | Deprecated alias for `secrets.runtime.name` | `""` |
| `s3.external.secret.keys.endpoint` | Deprecated alias for `secrets.runtime.keys.s3Endpoint` | `""` |
| `s3.external.secret.keys.accessKey` | Deprecated alias for `secrets.runtime.keys.accessKey` | `""` |
| `s3.external.secret.keys.secretKey` | Deprecated alias for `secrets.runtime.keys.secretKey` | `""` |
| `secrets.runtime.keys.s3Endpoint` | Key in runtime Secret for S3 endpoint | `"AWS_S3_ENDPOINT"` |
| `secrets.runtime.keys.accessKey` | Key in runtime Secret for S3 access key | `"AWS_ACCESS_KEY_ID"` |
| `secrets.runtime.keys.secretKey` | Key in runtime Secret for S3 secret key | `"AWS_SECRET_ACCESS_KEY"` |
| `secrets.runtime.keys.region` | Key in runtime Secret for AWS region | `"AWS_DEFAULT_REGION"` |
| `s3.external.enabled` | Use external S3 endpoint | `true` |
| `s3.workflowSecret.name` | Deprecated alias for `secrets.runtime.name` | `""` |
| `s3.workflowSecret.region` | Deprecated alias used for chart-managed region value | `""` |
| `s3.garage.enabled` | Deploy bundled Garage in-cluster | `false` |
| `config.workflow.workspace.mode` | Workspace storage mode (`auto|emptyDir|pvc`) | `"auto"` |
| `config.workflow.workspace.size` | Workspace PVC size request | `"30Gi"` |
| `config.workflow.workspace.storageClass` | Workspace PVC storage class (empty = unset) | `""` |
| `config.workflow.workspace.accessMode` | Workspace PVC access mode | `"ReadWriteOnce"` |
| `config.workflow.workspace.dynamicSize.enabled` | Enable dynamic PVC sizing (PVC mode only) | `false` |
| `config.workflow.workspace.dynamicSize.multiplier` | Workspace estimate multiplier | `4` |
| `config.workflow.workspace.dynamicSize.minGiB` | Dynamic workspace minimum GiB clamp | `30` |
| `config.workflow.workspace.dynamicSize.maxGiB` | Dynamic workspace maximum GiB clamp | `1024` |
| `config.workflow.workspace.dynamicSize.fallbackMBPerImage` | Fallback MB/image when byte totals unavailable | `20` |
| `config.ui.enabled` | Enable built-in `/ui` operator pages | `false` |
| `config.ui.readOnly` | Keep UI in read-only mode | `true` |
| `config.observability.enabled` | Enable OpenTelemetry bootstrap | `false` |
| `config.observability.otlpEndpoint` | OTLP gRPC endpoint for traces/metrics | `""` |
| `config.observability.metricsEnabled` | Enable OTel metrics export | `true` |
| `config.observability.tracesEnabled` | Enable OTel traces export | `true` |
| `config.observability.traceSampleRatio` | Trace sampling ratio (0.0-1.0) | `0.1` |
| `argo.enabled` | Deploy Argo Workflows subchart | `true` |
| `argo.controller.parallelism` | Max concurrent workflows (0 = unlimited) | `10` |
| `argo.namespaceOverride` | Namespace for Argo controller (subchart mode) | `argo` |
| `kubernetes.serviceAccount.create` | Create service account | `true` |
| `kubernetes.rbac.create` | Create RBAC resources | `true` |

See [values.yaml](values.yaml) for the complete list of configurable parameters.
