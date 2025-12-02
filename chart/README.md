# ScaleODM Helm Chart

Kubernetes-native auto-scaling and load balancing for OpenDroneMap.

## Prerequisites

- Kubernetes 1.19+
- Helm 3.0+
- Argo Workflows (can be installed via this chart or pre-installed)

## Installation

### Quick Start (OCI registry)

The ScaleODM chart is published as an **OCI Helm chart** at:

- `oci://ghcr.io/hotosm/charts/scaleodm`

You do **not** need to add a classic HTTP Helm repo; you can install directly from the OCI registry:

```bash
# Install the chart from OCI
helm install scaleodm oci://ghcr.io/hotosm/charts/scaleodm \
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
  --set database.external.enabled=true \
  --set database.external.secret.name="scaleodm-db-vars" \
  --set database.external.secret.key="SCALEODM_DATABASE_URL" \
  --set s3.external.secret.name="scaleodm-s3-vars" \
  --set argo.enabled=true
```

### Install with Argo Workflows Subchart

By default, Argo Workflows is installed as a subchart:

```bash
helm install scaleodm ./chart \
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
  --set database.external.enabled=true \
  --set database.external.secret.name="scaleodm-db-vars" \
  --set database.external.secret.key="SCALEODM_DATABASE_URL" \
  --set s3.external.secret.name="scaleodm-s3-vars" \
  --set argo.enabled=false \
  --set kubernetes.namespace="your-argo-namespace"
```

## Configuration

### Required Values

At least one database and one S3 configuration path must be provided:

- **Database** (choose one):
  - `database.external.enabled=true` with `database.external.secret.*` pointing at a Secret key that contains the full PostgreSQL URI (by default key `SCALEODM_DATABASE_URL`), or
  - `database.postgres.enabled=true` and corresponding `database.postgres.auth.*` for the bundled Postgres subchart.

- **S3**:
  - `s3.external.secret.name` must reference a Secret in the release namespace that contains at least an endpoint key (by default `SCALEODM_S3_ENDPOINT`) and optionally access/secret key and STS fields (`SCALEODM_S3_ACCESS_KEY`, `SCALEODM_S3_SECRET_KEY`, `SCALEODM_S3_STS_ENDPOINT`, `SCALEODM_S3_STS_ROLE_ARN`).

### Optional Values

See [values.yaml](values.yaml) for all available configuration options.

### Using AWS STS for S3

For better security, use AWS STS temporary credentials by placing the STS configuration in the same S3 Secret used above:

```bash
kubectl create secret generic s3-secret \
  --from-literal=SCALEODM_S3_ENDPOINT="s3.amazonaws.com" \
  --from-literal=SCALEODM_S3_ACCESS_KEY="your-iam-user-access-key" \
  --from-literal=SCALEODM_S3_SECRET_KEY="your-iam-user-secret-key" \
  --from-literal=SCALEODM_S3_STS_ROLE_ARN="arn:aws:iam::ACCOUNT_ID:role/scaleodm-workflow-role" \
  --from-literal=SCALEODM_S3_STS_ENDPOINT="https://sts.us-east-1.amazonaws.com"
```

See the main [README.md](../README.md) for detailed STS setup instructions.

### External Database

The chart supports external PostgreSQL databases via an existing Secret that contains the full connection string:

```bash
kubectl create secret generic scaleodm-db-vars \
  --from-literal=SCALEODM_DATABASE_URL="postgres://user:password@host:5432/scaleodm?sslmode=require"
```

Then install:

```bash
--set database.external.enabled=true \
--set database.external.secret.name="scaleodm-db-vars" \
--set database.external.secret.key="SCALEODM_DATABASE_URL"
```

### External S3

The chart supports external S3-compatible storage (AWS S3, external MinIO, etc.) via an existing Secret:

```bash
kubectl create secret generic scaleodm-s3-vars \
  --from-literal=SCALEODM_S3_ENDPOINT="s3.amazonaws.com" \
  --from-literal=SCALEODM_S3_ACCESS_KEY="your-access-key" \
  --from-literal=SCALEODM_S3_SECRET_KEY="your-secret-key"
```

Then install:

```bash
--set s3.external.enabled=true \
--set s3.external.secret.name="scaleodm-s3-vars"
```

### Bundled Postgres

To deploy a Postgres instance via the bundled Bitnami subchart:

```bash
--set database.postgres.enabled=true \
--set database.postgres.auth.password="your-password"
```

### Bundled MinIO

To deploy MinIO via the bundled Bitnami subchart:

```bash
--set s3.external.enabled=false \
--set s3.minio.enabled=true
```

## Argo Workflows Configuration

### Deploy Argo Workflows via Subchart

When `argo.enabled=true` (default), Argo Workflows is deployed as a subchart:

```yaml
argo:
  enabled: true
  namespace: argo
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

**Formula:** `parallelism = (number of worker nodes) × 2-3`

This accounts for:
- Each ODM workflow can be CPU/memory intensive
- Multiple workflows per node allows better resource utilization
- Prevents node exhaustion while maintaining throughput

**Note:** Set to `0` to disable the limit (not recommended for production as it can overwhelm nodes).

### Use Existing Argo Workflows

If Argo Workflows is already installed:

```yaml
argo:
  enabled: false

kubernetes:
  namespace: argo  # Namespace where Argo Workflows is installed
```

**Important:** If using an existing Argo Workflows installation, you'll need to manually configure parallelism by creating/updating the `workflow-controller-configmap` ConfigMap in the Argo namespace:

```bash
kubectl create configmap workflow-controller-configmap \
  --from-literal=parallelism=10 \
  -n argo
```

## Uninstallation

```bash
helm uninstall scaleodm
```

## Troubleshooting

### Check Pod Status

```bash
kubectl get pods -l app.kubernetes.io/name=scaleodm
```

### Check Logs

```bash
kubectl logs -l app.kubernetes.io/name=scaleodm
```

### Check Argo Workflows

```bash
kubectl get pods -n argo
kubectl get workflows -n argo
```

### Verify Database Connection

```bash
kubectl exec -it deployment/scaleodm -- env | grep SCALEODM_DATABASE_URL
```

### Verify S3 Configuration

```bash
kubectl exec -it deployment/scaleodm -- env | grep SCALEODM_S3
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
| `s3.external.secret.keys.stsEndpoint` | Key in the Secret for the STS endpoint | `"SCALEODM_S3_STS_ENDPOINT"` |
| `s3.external.secret.keys.stsRoleArn` | Key in the Secret for the STS role ARN | `"SCALEODM_S3_STS_ROLE_ARN"` |
| `s3.external.enabled` | Use external S3 endpoint | `true` |
| `s3.minio.enabled` | Deploy bundled MinIO subchart | `false` |
| `argo.enabled` | Deploy Argo Workflows subchart | `true` |
| `argo.controller.parallelism` | Max concurrent workflows (0 = unlimited) | `10` |
| `kubernetes.namespace` | Namespace for Argo Workflows | `argo` |
| `kubernetes.serviceAccount.create` | Create service account | `true` |
| `kubernetes.rbac.create` | Create RBAC resources | `true` |

See [values.yaml](values.yaml) for the complete list of configurable parameters.


