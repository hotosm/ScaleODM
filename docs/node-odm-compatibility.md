# NodeODM API Compatibility

ScaleODM implements the NodeODM API specification, making it a **drop-in replacement** for NodeODM with built-in scaling via Argo Workflows. Tools like **pyodm** and **WebODM** can connect to ScaleODM using the `zipurl` parameter for S3-based image processing.

## Implemented Endpoints

### Server Information

#### `GET /info`
Returns information about the node including queue count and engine version.

**Response:**
```json
{
  "version": "0.1.0",
  "taskQueueCount": 5,
  "maxImages": null,
  "engine": "odm",
  "engineVersion": "docker.io/opendronemap/odm:3.5.6"
}
```

#### `GET /options`
Returns available ODM processing options.

**Response:**
```json
[
  {
    "name": "fast-orthophoto",
    "type": "bool",
    "value": "false",
    "domain": "bool",
    "help": "Skips dense reconstruction and 3D model generation"
  },
  ...
]
```

### Task Management

#### `POST /task/new`
Creates a new processing task.

**Parameters (form or JSON body):**
- `name` (optional) - Task name (defaults to `odm-project`)
- `options` (optional) - JSON array of options: `[{"name": "fast-orthophoto", "value": true}]`
- `zipurl` - S3 path to input images (e.g., `s3://bucket/images/`). This is the **NodeODM-compatible** parameter — use this when working with pyodm or WebODM.
- `readS3Path` - S3 path to input images (preferred over `zipurl` for new integrations)
- `writeS3Path` (optional) - S3 path for outputs (defaults to `readS3Path/output/`)
- `s3Endpoint` (optional) - Custom S3-compatible endpoint (for MinIO, etc.)
- `s3Region` (optional) - S3 region (defaults to `us-east-1`)
- `webhook` (optional) - Callback URL when complete
- `skipPostProcessing` (optional) - Skip point cloud tiles
- `dateCreated` (optional) - Override creation timestamp

Either `zipurl` or `readS3Path` must be provided. Both must be `s3://` paths. HTTP zip URLs are not supported.

**Response:**
```json
{
  "uuid": "odm-pipeline-abc123"
}
```

**Example (curl):**
```bash
curl -X POST http://localhost:31100/task/new \
  -F "name=my-project" \
  -F "zipurl=s3://mybucket/drone-images/" \
  -F 'options=[{"name":"fast-orthophoto","value":true},{"name":"dsm","value":true}]'
```

#### `GET /task/list`
Lists all tasks.

**Response:**
```json
[
  {"uuid": "odm-pipeline-abc123"},
  {"uuid": "odm-pipeline-def456"}
]
```

#### `GET /task/{uuid}/info`
Gets detailed task information.

**Query Parameters:**
- `with_output` (optional) - Line number to start console output from

**Response:**
```json
{
  "uuid": "odm-pipeline-abc123",
  "name": "my-project",
  "dateCreated": 1705334400,
  "processingTime": 120000,
  "status": {
    "code": 20
  },
  "options": [
    {"name": "fast-orthophoto", "value": true}
  ],
  "imagesCount": 0,
  "progress": 50,
  "output": ["Starting ODM...", "Processing images..."]
}
```

The `status` field is a **nested object** matching the NodeODM spec (compatible with pyodm's `task.info['status']['code']`). For failed tasks, the status object includes an `errorMessage` field:

```json
{
  "status": {
    "code": 30,
    "errorMessage": "ODM processing failed: insufficient overlap between images"
  }
}
```

**Status Codes:**
- `10` = QUEUED (Pending in Argo)
- `20` = RUNNING (Running in Argo)
- `30` = FAILED (Failed/Error in Argo)
- `40` = COMPLETED (Succeeded in Argo)
- `50` = CANCELED (Deleted from Argo)

#### `GET /task/{uuid}/output`
Gets console output from the task.

**Query Parameters:**
- `line` (optional) - Line number to start from (default: 0)

**Response:**
```
Starting ODM processing...
Downloading images from S3...
Processing 45 images...
...
```

#### `POST /task/cancel`
Cancels a running task.

**Body:**
```json
{
  "uuid": "odm-pipeline-abc123"
}
```

**Response:**
```json
{
  "success": true
}
```

#### `POST /task/remove`
Removes a task and all its assets.

**Body:**
```json
{
  "uuid": "odm-pipeline-abc123"
}
```

#### `POST /task/restart`
Restarts a failed or completed task.

**Body:**
```json
{
  "uuid": "odm-pipeline-abc123",
  "options": "[{\"name\":\"dsm\",\"value\":true}]"
}
```

#### `GET /task/{uuid}/download/{asset}`
Downloads output assets. Redirects to a pre-signed S3 URL (valid for 1 hour).

**Assets:**
- `all.zip` - All outputs
- `orthophoto.tif` - Orthophoto
- `dsm.tif` - Digital Surface Model
- `dtm.tif` - Digital Terrain Model

## Using zipurl for NodeODM Compatibility (pyodm / WebODM)

ScaleODM supports the `zipurl` parameter from the NodeODM API, allowing pyodm and WebODM to create tasks without code changes — provided images are pre-uploaded to S3.

### How zipurl works in ScaleODM

When `POST /task/new` receives a `zipurl` parameter:

1. **S3 paths** (`s3://bucket/path/`) are accepted and converted internally:
   - `readS3Path` is set to the zipurl value
   - `writeS3Path` is set to `{zipurl}-output/` (e.g., `s3://bucket/path/-output/`)
2. **HTTP/HTTPS zip URLs** are **not supported** and return a `400` error. ScaleODM does not download and store images — it orchestrates processing of images already in S3.

### Using pyodm with ScaleODM

pyodm can connect to ScaleODM using the `zipurl` parameter. Upload images to S3 first, then pass the S3 path as `zipurl`:

```python
from pyodm import Node

node = Node("scaleodm-host", 31100)

# Images must already be in S3
task = node.create_task(
    [],  # No local files — images are in S3
    options={"fast-orthophoto": True, "dsm": True},
    zipurl="s3://mybucket/project/drone-images/"
)

# Poll for completion
info = task.info()
print(f"Status: {info.status.code}")  # 10=QUEUED, 20=RUNNING, 40=COMPLETED
print(f"Progress: {info.progress}%")

# Wait for completion
task.wait_for_completion()

# Download results (redirects to pre-signed S3 URL)
task.download_assets("/tmp/results/")
```

### Using WebODM with ScaleODM

WebODM can be configured to use ScaleODM as a processing node. Since WebODM normally uploads images directly, you need to:

1. Configure WebODM to use `zipurl` mode pointing to S3 paths
2. Upload images to S3 before creating the task
3. Point WebODM's processing node to ScaleODM's address

### Using readS3Path (preferred for new integrations)

For new integrations that don't need NodeODM compatibility, use `readS3Path` and `writeS3Path` directly for more control:

```python
import requests, json

response = requests.post(
    "http://scaleodm:31100/task/new",
    json={
        "name": "my-project",
        "readS3Path": "s3://mybucket/project/input/",
        "writeS3Path": "s3://mybucket/project/output/",
        "s3Region": "us-east-1",
        "options": json.dumps([
            {"name": "fast-orthophoto", "value": True},
            {"name": "dsm", "value": True}
        ])
    }
)
task_uuid = response.json()["uuid"]
```

## Key Differences from Standard NodeODM

### 1. S3-Based Workflow
Instead of uploading files via multipart/form-data:
- Use `zipurl` (S3 path) or `readS3Path` parameter
- Images must be pre-uploaded to S3
- Outputs are written to S3 automatically
- Download endpoint redirects to pre-signed S3 URLs

**Example S3 structure:**
```
s3://mybucket/
  └── project-123/
      ├── input/          # Source images (zipurl/readS3Path points here)
      │   ├── img1.jpg
      │   ├── img2.jpg
      │   └── ...
      └── output/         # ODM outputs (auto-created)
          ├── orthophoto.tif
          ├── dsm.tif
          └── ...
```

### 2. UUID = Argo Workflow Name
- NodeODM UUIDs map to Argo workflow names
- Format: `odm-pipeline-abc123`
- Trackable via `kubectl get workflows`

### 3. Status Mapping

| Argo Phase | NodeODM Code | NodeODM Status |
|-----------|--------------|----------------|
| Pending   | 10          | QUEUED         |
| Running   | 20          | RUNNING        |
| Succeeded | 40          | COMPLETED      |
| Failed    | 30          | FAILED         |
| Error     | 30          | FAILED         |

### 4. Download Behavior
The `/task/{uuid}/download/{asset}` endpoint returns an HTTP 302 redirect to a pre-signed S3 URL (valid for 1 hour). Most HTTP clients (including pyodm) follow redirects automatically.

### 5. Authentication
The `token` query parameter is accepted but not enforced. Add authentication via:
- API Gateway in front of ScaleODM
- Kubernetes Ingress with auth
- Service mesh policies

## Testing NodeODM Compatibility

```bash
# Test /info endpoint
curl http://localhost:31100/info

# Create a task using zipurl (NodeODM-compatible)
curl -X POST http://localhost:31100/task/new \
  -F "zipurl=s3://test-bucket/images/" \
  -F 'options=[{"name":"fast-orthophoto","value":true}]'

# Create a task using readS3Path (ScaleODM native)
curl -X POST http://localhost:31100/task/new \
  -H "Content-Type: application/json" \
  -d '{
    "readS3Path": "s3://test-bucket/images/",
    "writeS3Path": "s3://test-bucket/output/",
    "options": "[{\"name\":\"fast-orthophoto\",\"value\":true}]"
  }'

# Get task info (status is nested object: {"code": 20})
curl http://localhost:31100/task/{uuid}/info

# Get task output
curl http://localhost:31100/task/{uuid}/output

# List all tasks
curl http://localhost:31100/task/list

# Cancel a task
curl -X POST http://localhost:31100/task/cancel \
  -H "Content-Type: application/json" \
  -d '{"uuid": "odm-pipeline-abc123"}'

# Download an asset (follows redirect to S3)
curl -L http://localhost:31100/task/{uuid}/download/orthophoto.tif -o orthophoto.tif
```

## Not Implemented (Yet)

- File uploads via `/task/new/init`, `/task/new/upload`, `/task/new/commit` (chunked upload flow)
- HTTP zip URL downloads (only S3 paths are supported for `zipurl`)
- Authentication endpoints (`/auth/login`, `/auth/register`, `/auth/info`)
- Image count tracking (`imagesCount` always returns 0)

These can be added if needed, but the core S3-based workflow is more efficient for production use.
