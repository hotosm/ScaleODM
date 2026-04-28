# ScaleODM

Kubernetes-native orchestration for OpenDroneMap workloads, with a NodeODM-compatible API and S3-native task I/O.

## Security

> [!WARNING]
> ScaleODM does not provide authentication or authorization.
> Run it only on trusted private/internal networks.
> Do not expose the API directly to the public internet.

## Quick start (Helm OCI)

This is the shortest path from zero to a running install on an existing Kubernetes cluster.

1. Create namespace and required runtime secret:

```bash
kubectl create namespace scaleodm

kubectl create secret generic scaleodm-secrets \
  --namespace scaleodm \
  --from-literal=SCALEODM_DATABASE_URL="postgresql://user:pass@your-db:5432/scaleodm?sslmode=require" \
  --from-literal=AWS_S3_ENDPOINT="https://s3.amazonaws.com" \
  --from-literal=AWS_ACCESS_KEY_ID="YOUR_ACCESS_KEY" \
  --from-literal=AWS_SECRET_ACCESS_KEY="YOUR_SECRET_KEY" \
  --from-literal=AWS_DEFAULT_REGION="us-east-1"
```

2. Install the chart:

```bash
helm upgrade --install scaleodm oci://ghcr.io/hotosm/charts/scaleodm \
  --namespace scaleodm \
  --create-namespace \
  --version <chart-version> \
  --set database.external.enabled=true \
  --set s3.external.enabled=true \
  --set secrets.runtime.name="scaleodm-secrets" \
  --set argo.enabled=true
```

3. Verify API connectivity (adjust host/port for your service exposure):

```bash
curl http://localhost:31100/info
```

## API smoke test

```bash
# 1) Node info
curl http://localhost:31100/info

# 2) Create task (images must already exist in S3)
curl -X POST http://localhost:31100/task/new \
  -F "name=my-project" \
  -F "readS3Path=s3://mybucket/images/" \
  -F "writeS3Path=s3://mybucket/images/output/" \
  -F 'options=[{"name":"fast-orthophoto","value":true}]'

# 3) Check task status
curl http://localhost:31100/task/{uuid}/info
```

## Documentation index

- [About / project rationale](./docs/about.md)
- [pyodm quick migration guide](./docs/nodeodm-migrate.md)
- [NodeODM compatibility reference](./docs/nodeodm-compatibility.md)
- [Helm chart deployment/configuration reference](./chart/README.md)
- [Testing guide](./docs/testing.md)
