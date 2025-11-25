# ScaleODM Helm Chart

Kubernetes-native auto-scaling and load balancing for OpenDroneMap.

## Prerequisites

- Kubernetes 1.19+
- Helm 3.0+
- Argo Workflows (can be installed via this chart or pre-installed)

## Installation

### Quick Start

```bash
# Add the repository (if publishing to a Helm repo)
helm repo add scaleodm https://charts.example.com/scaleodm
helm repo update

# Install with external database and S3
helm install scaleodm ./chart \
  --set secrets.databaseUrl="postgres://user:password@host:5432/scaleodm" \
  --set secrets.s3Endpoint="s3.amazonaws.com" \
  --set secrets.s3AccessKey="your-access-key" \
  --set secrets.s3SecretKey="your-secret-key" \
  --set argo.enabled=true
```

### Install with Argo Workflows Subchart

By default, Argo Workflows is installed as a subchart:

```bash
helm install scaleodm ./chart \
  --set secrets.databaseUrl="postgres://user:password@host:5432/scaleodm" \
  --set secrets.s3Endpoint="s3.amazonaws.com" \
  --set secrets.s3AccessKey="your-access-key" \
  --set secrets.s3SecretKey="your-secret-key" \
  --set argo.enabled=true
```

### Install without Argo Workflows Subchart

If Argo Workflows is already installed in your cluster:

```bash
helm install scaleodm ./chart \
  --set secrets.databaseUrl="postgres://user:password@host:5432/scaleodm" \
  --set secrets.s3Endpoint="s3.amazonaws.com" \
  --set secrets.s3AccessKey="your-access-key" \
  --set secrets.s3SecretKey="your-secret-key" \
  --set argo.enabled=false \
  --set kubernetes.namespace="your-argo-namespace"
```

## Configuration

### Required Values

The following values must be provided:

- `secrets.databaseUrl`: PostgreSQL connection string
- `secrets.s3Endpoint`: S3 endpoint URL
- `secrets.s3AccessKey`: S3 access key (optional if using public buckets)
- `secrets.s3SecretKey`: S3 secret key (optional if using public buckets)

### Optional Values

See [values.yaml](values.yaml) for all available configuration options.

### Using AWS STS for S3

For better security, use AWS STS temporary credentials:

```bash
helm install scaleodm ./chart \
  --set secrets.databaseUrl="postgres://user:password@host:5432/scaleodm" \
  --set secrets.s3Endpoint="s3.amazonaws.com" \
  --set secrets.s3AccessKey="your-iam-user-access-key" \
  --set secrets.s3SecretKey="your-iam-user-secret-key" \
  --set secrets.s3StsRoleArn="arn:aws:iam::ACCOUNT_ID:role/scaleodm-workflow-role" \
  --set secrets.s3StsEndpoint="https://sts.us-east-1.amazonaws.com"
```

See the main [README.md](../README.md) for detailed STS setup instructions.

### External Database

The chart supports external PostgreSQL databases. Provide the connection string:

```bash
--set secrets.databaseUrl="postgres://user:password@host:5432/scaleodm?sslmode=require"
```

### External S3

The chart supports external S3-compatible storage (AWS S3, MinIO, etc.):

```bash
--set secrets.s3Endpoint="s3.amazonaws.com"
--set secrets.s3AccessKey="your-access-key"
--set secrets.s3SecretKey="your-secret-key"
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
```

### Use Existing Argo Workflows

If Argo Workflows is already installed:

```yaml
argo:
  enabled: false

kubernetes:
  namespace: argo  # Namespace where Argo Workflows is installed
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
| `api.enabled` | Enable ScaleODM API | `true` |
| `api.image.repository` | Image repository | `ghcr.io/hotosm/scaleodm` |
| `api.image.tag` | Image tag | `""` (uses Chart.AppVersion) |
| `api.replicaCount` | Number of replicas | `1` |
| `api.service.type` | Service type | `ClusterIP` |
| `api.service.port` | Service port | `31100` |
| `secrets.databaseUrl` | PostgreSQL connection string | **Required** |
| `secrets.s3Endpoint` | S3 endpoint | **Required** |
| `secrets.s3AccessKey` | S3 access key | `""` |
| `secrets.s3SecretKey` | S3 secret key | `""` |
| `argo.enabled` | Deploy Argo Workflows subchart | `true` |
| `kubernetes.namespace` | Namespace for Argo Workflows | `argo` |
| `kubernetes.serviceAccount.create` | Create service account | `true` |
| `kubernetes.rbac.create` | Create RBAC resources | `true` |

See [values.yaml](values.yaml) for the complete list of configurable parameters.

