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
- **One S3 secret** in the release namespace serves both the API server and workflow pods
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
  --set database.external.secret.name="scaleodm-db-vars" \
  --set database.external.secret.key="SCALEODM_DATABASE_URL" \
  --set s3.external.secret.name="scaleodm-s3-vars" \
  --set argo.enabled=true
```

Replace `<chart-version>` with the desired chart version (e.g. the latest release).

### Local Chart Development Install

```bash
# Install from the local `./chart` directory
helm install scaleodm ./chart \
  --namespace scaleodm --create-namespace \
  --set database.external.enabled=true \
  --set database.external.secret.name="scaleodm-db-vars" \
  --set database.external.secret.key="SCALEODM_DATABASE_URL" \
  --set s3.external.secret.name="scaleodm-s3-vars" \
  --set argo.enabled=true
```

### Install with Argo Workflows Subchart

By default, Argo Workflows is installed as a subchart in the `argo` namespace:

```bash
helm install scaleodm ./chart \
  --namespace scaleodm --create-namespace \
  --set database.external.enabled=true \
  --set database.external.secret.name="scaleodm-db-vars" \
  --set database.external.secret.key="SCALEODM_DATABASE_URL" \
  --set s3.external.secret.name="scaleodm-s3-vars" \
  --set argo.enabled=true
```

### Install without Argo Workflows Subchart

If Argo Workflows is already installed in your cluster:

```bash
helm install scaleodm ./chart \
  --namespace scaleodm --create-namespace \
  --set database.external.enabled=true \
  --set database.external.secret.name="scaleodm-db-vars" \
  --set database.external.secret.key="SCALEODM_DATABASE_URL" \
  --set s3.external.secret.name="scaleodm-s3-vars" \
  --set argo.enabled=false
```

## Configuration

### Required Values

At least one database and one S3 configuration path must be provided:

- **Database** (choose one):
  - `database.external.enabled=true` with `database.external.secret.*` pointing at a Secret key that contains the full PostgreSQL URI (by default key `SCALEODM_DATABASE_URL`), or
  - `database.postgres.enabled=true` and corresponding `database.postgres.auth.*` for the bundled Postgres subchart.

- **S3** (two secrets required in the release namespace):
  - `s3.external.secret.name` — Secret containing `SCALEODM_S3_ENDPOINT`, `SCALEODM_S3_ACCESS_KEY`, and `SCALEODM_S3_SECRET_KEY` (used by the API server).
  - `s3.workflowSecret.name` — Secret containing `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, and `AWS_DEFAULT_REGION` (used by workflow pods via `secretKeyRef`).

Both secrets live in the release namespace. If you use the same credentials for both, you can point them at the same secret (provided the key names match).

### Optional Values

See [values.yaml](values.yaml) for all available configuration options.

### External Database

The chart supports external PostgreSQL databases via an existing Secret that contains the full connection string:

```bash
kubectl create secret generic scaleodm-db-vars \
  -n scaleodm \
  --from-literal=SCALEODM_DATABASE_URL="postgres://user:password@host:5432/scaleodm?sslmode=require"
```

Then install:

```bash
--set database.external.enabled=true \
--set database.external.secret.name="scaleodm-db-vars" \
--set database.external.secret.key="SCALEODM_DATABASE_URL"
```

### External S3

The chart supports external S3-compatible storage (AWS S3, MinIO, Garage, etc.).
Create two secrets in the release namespace:

```bash
# API server credentials
kubectl create secret generic scaleodm-s3-vars \
  -n scaleodm \
  --from-literal=SCALEODM_S3_ENDPOINT="s3.amazonaws.com" \
  --from-literal=SCALEODM_S3_ACCESS_KEY="your-access-key" \
  --from-literal=SCALEODM_S3_SECRET_KEY="your-secret-key"

# Workflow pod credentials (referenced via secretKeyRef in workflow specs)
kubectl create secret generic scaleodm-s3-creds \
  -n scaleodm \
  --from-literal=AWS_ACCESS_KEY_ID="your-access-key" \
  --from-literal=AWS_SECRET_ACCESS_KEY="your-secret-key" \
  --from-literal=AWS_DEFAULT_REGION="us-east-1"
```

Then install:

```bash
--set s3.external.enabled=true \
--set s3.external.secret.name="scaleodm-s3-vars" \
--set s3.workflowSecret.name="scaleodm-s3-creds"
```

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

**Note:** Set to `0` to disable the limit (not recommended for production as it can overwhelm nodes).

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
| `database.external.enabled` | Use external PostgreSQL via existing Secret | `true` |
| `database.external.secret.name` | Name of the Secret containing the DB URI | `"scaleodm-db-vars"` |
| `database.external.secret.key` | Key in the Secret that stores the DB URI | `"SCALEODM_DATABASE_URL"` |
| `database.postgres.enabled` | Deploy bundled Postgres subchart | `false` |
| `s3.external.secret.name` | Name of the Secret containing S3 configuration | `"scaleodm-s3-vars"` |
| `s3.external.secret.keys.endpoint` | Key in the Secret for the S3 endpoint | `"SCALEODM_S3_ENDPOINT"` |
| `s3.external.secret.keys.accessKey` | Key in the Secret for the S3 access key | `"SCALEODM_S3_ACCESS_KEY"` |
| `s3.external.secret.keys.secretKey` | Key in the Secret for the S3 secret key | `"SCALEODM_S3_SECRET_KEY"` |
| `s3.external.enabled` | Use external S3 endpoint | `true` |
| `s3.workflowSecret.name` | Workflow pod S3 secret name (in release namespace) | `"scaleodm-s3-creds"` |
| `s3.workflowSecret.region` | Default AWS region stored in workflow secret (Garage mode) | `"us-east-1"` |
| `s3.garage.enabled` | Deploy bundled Garage in-cluster | `false` |
| `argo.enabled` | Deploy Argo Workflows subchart | `true` |
| `argo.controller.parallelism` | Max concurrent workflows (0 = unlimited) | `10` |
| `argo.namespaceOverride` | Namespace for Argo controller (subchart mode) | `argo` |
| `kubernetes.serviceAccount.create` | Create service account | `true` |
| `kubernetes.rbac.create` | Create RBAC resources | `true` |

See [values.yaml](values.yaml) for the complete list of configurable parameters.
