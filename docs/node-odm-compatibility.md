# NodeODM API Compatibility

ScaleODM now implements the NodeODM API specification, making it a **drop-in replacement** for NodeODM with built-in scaling via Argo Workflows.

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
  "engineVersion": "opendronemap/odm:3.6.0"
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

**Parameters:**
- `name` (optional) - Task name
- `options` (optional) - JSON array of options: `[{"name": "fast-orthophoto", "value": true}]`
- `zipurl` (required) - S3 path to input images (e.g., `s3://bucket/images/`)
- `webhook` (optional) - Callback URL when complete
- `skipPostProcessing` (optional) - Skip point cloud tiles

**Response:**
```json
{
  "uuid": "odm-pipeline-abc123"
}
```

**Example:**
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
Downloads output assets (currently returns S3 path).

**Assets:**
- `all.zip` - All outputs
- `orthophoto.tif` - Orthophoto
- `dsm.tif` - Digital Surface Model
- `dtm.tif` - Digital Terrain Model

## Key Differences from Standard NodeODM

### 1. **S3-Based Workflow**
Instead of uploading files via multipart/form-data:
- Use `zipurl` parameter pointing to S3 path
- Images must be pre-uploaded to S3
- Outputs are written to S3 automatically

**Example S3 structure:**
```
s3://mybucket/
  └── project-123/
      ├── input/          # Source images (zipurl points here)
      │   ├── img1.jpg
      │   ├── img2.jpg
      │   └── ...
      └── output/         # ODM outputs (auto-created)
          ├── orthophoto.tif
          ├── dsm.tif
          └── ...
```

### 2. **UUID = Argo Workflow Name**
- NodeODM UUIDs map to Argo workflow names
- Format: `odm-pipeline-abc123`
- Trackable via `kubectl get workflows`

### 3. **Status Mapping**

| Argo Phase | NodeODM Code | NodeODM Status |
|-----------|--------------|----------------|
| Pending   | 10          | QUEUED         |
| Running   | 20          | RUNNING        |
| Succeeded | 40          | COMPLETED      |
| Failed    | 30          | FAILED         |
| Error     | 30          | FAILED         |

### 4. **Download Behavior**
Direct file downloads are not implemented. Instead:
- Files are stored in S3 at the configured `write_s3_path`
- Download endpoint returns S3 path for external access
- Use AWS CLI or SDK to retrieve files from S3

### 5. **Authentication**
The `token` query parameter is accepted but not enforced. Add authentication via:
- API Gateway in front of ScaleODM
- Kubernetes Ingress with auth
- Service mesh policies

## Migration from NodeODM

### Step 1: Update Client Code
Change from multipart upload to S3 reference:

**Before (NodeODM):**
```python
files = {'images': open('img1.jpg', 'rb')}
response = requests.post(
    'http://nodeodm:3000/task/new',
    files=files,
    data={'options': json.dumps([{"name": "dsm", "value": true}])}
)
```

**After (ScaleODM):**
```python
# Upload images to S3 first
s3.upload_file('img1.jpg', 'mybucket', 'project/input/img1.jpg')

# Create task pointing to S3
response = requests.post(
    'http://scaleodm:31100/task/new',
    data={
        'zipurl': 's3://mybucket/project/input/',
        'options': json.dumps([{"name": "dsm", "value": true}])
    }
)
```

### Step 2: Update Result Retrieval

**Before (NodeODM):**
```python
# Download directly from NodeODM
response = requests.get(f'http://nodeodm:3000/task/{uuid}/download/all.zip')
with open('results.zip', 'wb') as f:
    f.write(response.content)
```

**After (ScaleODM):**
```python
# Get task info to determine S3 path
info = requests.get(f'http://scaleodm:31100/task/{uuid}/info').json()

# Download from S3
s3.download_file('mybucket', 'project/output/orthophoto.tif', 'orthophoto.tif')
```

### Step 3: Deploy ScaleODM

```yaml
# kubernetes/scaleodm-deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: scaleodm
spec:
  replicas: 2
  selector:
    matchLabels:
      app: scaleodm
  template:
    metadata:
      labels:
        app: scaleodm
    spec:
      serviceAccountName: argo-odm
      containers:
      - name: scaleodm
        image: ghcr.io/hotosm/scaleodm:3.6.0
        ports:
        - containerPort: 31100
        env:
        - name: SCALEODM_DATABASE_URL
          value: "postgresql://user:pass@localhost:5432/scaleodm"
        - name: K8S_NAMESPACE
          value: "default"
        - name: SCALEODM_ODM_IMAGE
          value: "opendronemap/odm:3.6.0"
---
apiVersion: v1
kind: Service
metadata:
  name: scaleodm
spec:
  selector:
    app: scaleodm
  ports:
  - port: 80
    targetPort: 31100
```

## Testing NodeODM Compatibility

```bash
# Test /info endpoint
curl http://localhost:31100/info

# Create a task
curl -X POST http://localhost:31100/task/new \
  -F "zipurl=s3://test-bucket/images/" \
  -F 'options=[{"name":"fast-orthophoto","value":true}]'

# Get task info
curl http://localhost:31100/task/{uuid}/info

# Get task output
curl http://localhost:31100/task/{uuid}/output

# List all tasks
curl http://localhost:31100/task/list

# Cancel a task
curl -X POST http://localhost:31100/task/cancel \
  -H "Content-Type: application/json" \
  -d '{"uuid": "odm-pipeline-abc123"}'
```

## Advantages Over NodeODM

1. **Horizontal Scaling** - Multiple API servers, Kubernetes handles job scheduling
2. **Resource Efficiency** - Jobs run as Argo workflows with proper resource limits
3. **Queue Management** - Kubernetes scheduler handles queuing automatically
4. **Monitoring** - Native Kubernetes monitoring and logging
5. **High Availability** - Stateless API servers, jobs survive API restarts
6. **Multi-cluster** - Can federate across multiple Kubernetes clusters

## Not Implemented (Yet)

- File uploads via `/task/new/init`, `/task/new/upload`, `/task/new/commit`
- Direct file downloads from `/task/{uuid}/download/{asset}`
- Authentication endpoints (`/auth/login`, `/auth/register`, `/auth/info`)
- Image count tracking (`imagesCount` always returns 0)

These can be added if needed, but the core S3-based workflow is more efficient for production use.
