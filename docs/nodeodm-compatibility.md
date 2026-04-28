# NodeODM API Compatibility

ScaleODM implements the NodeODM API specification with S3-native storage, making it a scalable replacement for NodeODM via Argo Workflows.

Looking for the shortest pyodm migration path first? Start with the [quick migration guide](./nodeodm-migrate.md), then return here for full endpoint and behavior reference details.

## Quick Start: Deploy to Existing Cluster

If you already have Argo Workflows installed in an `argo` namespace:

```bash
# 1. Create the namespace
kubectl create namespace scaleodm

# 2. Create the required runtime secret
kubectl create secret generic scaleodm-secrets \
  --namespace scaleodm \
  --from-literal=SCALEODM_DATABASE_URL="postgresql://user:pass@your-db:5432/scaleodm?sslmode=require" \
  --from-literal=AWS_S3_ENDPOINT="https://s3.amazonaws.com" \
  --from-literal=AWS_ACCESS_KEY_ID="YOUR_ACCESS_KEY" \
  --from-literal=AWS_SECRET_ACCESS_KEY="YOUR_SECRET_KEY" \
  --from-literal=AWS_DEFAULT_REGION="us-east-1"

# 3. Install the chart (skip Argo subchart since you already have it)
helm install scaleodm ./chart \
  --namespace scaleodm \
  --set argo.enabled=false \
  --set database.external.enabled=true \
  --set s3.external.enabled=true \
  --set secrets.runtime.name="scaleodm-secrets"

# 4. Verify
kubectl get pods -n scaleodm
curl http://scaleodm.scaleodm.svc.cluster.local:31100/info
```

The API is now available at `scaleodm.scaleodm.svc.cluster.local:31100` from within the cluster.

## How It Works

The processing pipeline for each task:

1. **Task creation** - `POST /task/new` with an S3 path (via `zipurl` or `readS3Path`)
2. **Download stage** - Argo workflow pod uses rclone to copy images from S3 to local disk. Supports raw images (jpg/tif) and archives (zip/tar/tar.gz) which are auto-extracted and flattened.
3. **Process stage** - ODM container processes the images
4. **Upload stage** - rclone pushes results back to S3 (to `writeS3Path` or `{zipurl}-output/`)
5. **Query results** - `GET /task/{uuid}/info` for status, `GET /task/{uuid}/assets` for output discovery, and `GET /task/{uuid}/download/{asset}` for the canonical 302 redirect flow to pre-signed S3 URLs

## Using with pyodm

pyodm's `create_task()` uses NodeODM's chunked upload flow (`/task/new/init` + `/task/new/upload` + `/task/new/commit`) which ScaleODM does not implement - ScaleODM processes images already in S3.

However, pyodm works with ScaleODM for **task creation via `Node.post()`** and for **all monitoring/download operations** via the standard `Task` class. No custom wrapper package needed.

### Create tasks with pyodm + S3

```python
import json
from pyodm import Node
from pyodm.api import Task

node = Node("scaleodm.scaleodm.svc.cluster.local", 31100)

# Create task - use Node.post() to send zipurl directly
result = node.post("/task/new", data={
    "name": "my-project",
    "zipurl": "s3://mybucket/drone-images/",
    "options": json.dumps([
        {"name": "fast-orthophoto", "value": True},
        {"name": "dsm", "value": True},
    ]),
})
uuid = result.json()["uuid"]
print(f"Task created: {uuid}")

# Wrap in a pyodm Task for monitoring - all standard methods work
task = Task(node, uuid)

# Poll status
info = task.info()
print(f"Status: {info.status}")       # TaskStatus enum
print(f"Progress: {info.progress}%")

# Block until complete
task.wait_for_completion()

# Download results (follows redirect to pre-signed S3 URL)
task.download_assets("/tmp/results/")
```

### Helper function for cleaner integration

If you're replacing NodeODM in an existing app, add this helper:

```python
import json
from pyodm import Node
from pyodm.api import Task


def create_s3_task(node, s3_path, name="odm-project", options=None):
    """Create a ScaleODM task from an S3 path of images.

    Args:
        node: pyodm Node pointed at ScaleODM
        s3_path: S3 path containing images (e.g. "s3://bucket/images/")
        name: Project name
        options: Dict of ODM options (e.g. {"dsm": True})

    Returns:
        pyodm Task object (supports .info(), .wait_for_completion(),
        .download_assets(), etc.)
    """
    odm_options = []
    if options:
        for k, v in options.items():
            odm_options.append({"name": k, "value": v})

    data = {
        "name": name,
        "zipurl": s3_path,
    }
    if odm_options:
        data["options"] = json.dumps(odm_options)

    result = node.post("/task/new", data=data)
    return Task(node, result.json()["uuid"])


# Usage - almost identical to pyodm's create_task():
node = Node("scaleodm-host", 31100)
task = create_s3_task(
    node,
    "s3://mybucket/drone-images/",
    name="my-project",
    options={"fast-orthophoto": True, "dsm": True},
)
task.wait_for_completion()
task.download_assets("/tmp/results/")
```

### Migration from NodeODM

Replace your existing `create_task()` calls:

```python
# BEFORE (NodeODM - uploads files directly):
task = node.create_task(
    ["img1.jpg", "img2.jpg", ...],
    options={"dsm": True},
)

# AFTER (ScaleODM - images pre-uploaded to S3):
task = create_s3_task(
    node,
    "s3://mybucket/project/images/",
    options={"dsm": True},
)

# Everything else stays the same:
task.wait_for_completion()
task.download_assets("/tmp/results/")
info = task.info()
task.cancel()
task.remove()
```

## Implemented Endpoints

### Server Information

#### `GET /info`
Returns node information including queue count and engine version.

```json
{
  "version": "0.3.0",
  "taskQueueCount": 5,
  "maxImages": null,
  "engine": "odm",
  "engineVersion": "docker.io/opendronemap/odm:3.5.6"
}
```

#### `GET /options`
Returns available ODM processing options.

### Task Management

#### `POST /task/new`
Creates a new processing task.

**Parameters (form-encoded or JSON body):**

| Parameter | Required | Description |
|-----------|----------|-------------|
| `zipurl` | * | S3 path to images (e.g. `s3://bucket/images/`). NodeODM-compatible. |
| `readS3Path` | * | S3 path to images. Preferred for new integrations. |
| `writeS3Path` | | S3 path for outputs. Defaults to `readS3Path/output/`. |
| `name` | | Task name. Defaults to `odm-project`. |
| `options` | | JSON array: `[{"name": "dsm", "value": true}]` |
| `s3Endpoint` | | Custom S3 endpoint (MinIO, Garage, etc.). Must be reachable from workflow pods. |
| `s3Region` | | S3 region. Defaults to `us-east-1`. |
| `webhook` | | Callback URL on completion. |
| `skipPostProcessing` | | Skip point cloud tiles. |

\* One of `zipurl` or `readS3Path` is required. Both must be `s3://` paths.

When using `zipurl`, outputs are written to `{zipurl}-output/`.
When using `readS3Path`, outputs default to `readS3Path/output/` (or explicit `writeS3Path`).

If `s3Endpoint` is provided, ScaleODM applies that endpoint to workflow pods and API-side
S3 operations (image counting, log fallback, and pre-signed downloads). Endpoints are
normalized to scheme+host[:port] and local S3-compatible systems use path-style bucket
addressing.

**Response:** `{"uuid": "odm-pipeline-abc123"}`

#### `GET /task/{uuid}/info`
Returns task status. The `status` field is a nested object matching NodeODM spec:

```json
{
  "uuid": "odm-pipeline-abc123",
  "name": "my-project",
  "dateCreated": 1705334400,
  "processingTime": 120000,
  "status": {"code": 20},
  "options": [{"name": "fast-orthophoto", "value": true}],
  "imagesCount": 0,
  "progress": 50,
  "output": ["Processing images..."]
}
```

Failed tasks include an error message: `{"status": {"code": 30, "errorMessage": "..."}}`

**Status codes:** 10=QUEUED, 20=RUNNING, 30=FAILED, 40=COMPLETED, 50=CANCELED

#### `GET /task/{uuid}/output`
Console output. Query param `line` to start from a specific line.

#### `GET /task/{uuid}/assets`
Additive convenience endpoint for explicit output discovery.

Query params:

- `includeAdditional` (default `false`): when true, includes discovered non-primary files.
- `additionalLimit` (default `100`, max `1000`): cap for additional discovered files.

Response shape:

```json
{
  "primary": [
    {
      "id": "all_zip",
      "asset": "all.zip",
      "exists": true,
      "downloadUrl": "/task/odm-pipeline-abc/download/all.zip"
    },
    {
      "id": "point_cloud",
      "asset": "georeferenced_model.laz",
      "exists": false
    }
  ],
  "additional": [
    {
      "asset": "report.pdf",
      "downloadUrl": "/task/odm-pipeline-abc/download/report.pdf"
    }
  ]
}
```

Primary assets are always returned as exists/missing using a bounded check set (`all.zip`, `orthophoto.tif`, `dsm.tif`, `dtm.tif`, and first-match point-cloud candidates).

#### `GET /task/{uuid}/download/{asset}`
Canonical download endpoint. Returns HTTP 302 redirect to a pre-signed S3 URL (1 hour expiry). The new `/assets` endpoint returns URLs pointing here (not direct pre-signed URLs), so existing redirect semantics remain unchanged.

#### `POST /task/cancel`
Body: `{"uuid": "..."}` → `{"success": true}`

#### `POST /task/remove`
Body: `{"uuid": "..."}` → `{"success": true}`

#### `POST /task/restart`
Body: `{"uuid": "...", "options": "[...]"}` → `{"success": true}`

## Key Differences from NodeODM

| | NodeODM | ScaleODM |
|---|---------|----------|
| **Image input** | Upload via multipart form | S3 path via `zipurl` or `readS3Path` |
| **Chunked upload** | `/task/new/init` + `/upload` + `/commit` | Not implemented (not needed) |
| **Downloads** | Direct binary response | 302 redirect to pre-signed S3 URL |
| **UUIDs** | Random UUID | Argo workflow name (`odm-pipeline-xxxxx`) |
| **Scaling** | Single machine | Kubernetes + Argo Workflows |
| **Image formats** | Upload any file | S3 dir with jpg/tif (or zip/tar archives) |

### Status Mapping

| Argo Phase | NodeODM Code | Status |
|-----------|--------------|--------|
| Pending   | 10          | QUEUED |
| Running   | 20          | RUNNING |
| Succeeded | 40          | COMPLETED |
| Failed    | 30          | FAILED |
| Error     | 30          | FAILED |

## Deployment Reference

### Helm Values for External Argo + External DB + External S3

```yaml
# values-production.yaml
argo:
  enabled: false  # Use your existing Argo Workflows installation

database:
  external:
    enabled: true

s3:
  external:
    enabled: true

secrets:
  runtime:
    name: "scaleodm-secrets"
    keys:
      databaseUrl: "SCALEODM_DATABASE_URL"
      s3Endpoint: "AWS_S3_ENDPOINT"
      accessKey: "AWS_ACCESS_KEY_ID"
      secretKey: "AWS_SECRET_ACCESS_KEY"
      region: "AWS_DEFAULT_REGION"

api:
  replicaCount: 2  # HA - also creates a PodDisruptionBudget

config:
  odmImage: "docker.io/opendronemap/odm:3.5.6"
```

```bash
helm install scaleodm ./chart -n scaleodm -f values-production.yaml
```

### S3 Structure

```
s3://mybucket/
  └── project-123/
      ├── images/            ← zipurl/readS3Path points here
      │   ├── DJI_0001.jpg
      │   ├── DJI_0002.jpg
      │   └── ...
      └── images-output/     ← auto-created by ScaleODM (zipurl mode)
          ├── odm_orthophoto/
          │   └── odm_orthophoto.tif
          ├── odm_dem/
          │   ├── dsm.tif
          │   └── dtm.tif
          └── ...
```

### Testing

```bash
# Verify API is up
curl http://scaleodm:31100/info

# Create a task
curl -X POST http://scaleodm:31100/task/new \
  -F "zipurl=s3://mybucket/images/" \
  -F 'options=[{"name":"fast-orthophoto","value":true}]'

# Check status
curl http://scaleodm:31100/task/odm-pipeline-xxxxx/info

# Download (follows redirect)
curl -L http://scaleodm:31100/task/odm-pipeline-xxxxx/download/orthophoto.tif -o orthophoto.tif
```

## Not Implemented

- File uploads via `/task/new/init`, `/task/new/upload`, `/task/new/commit` (chunked upload)
- HTTP zip URL downloads (only `s3://` paths supported)
- Auth endpoints (`/auth/login`, `/auth/info`)
- `imagesCount` (always 0)
