# pyodm migration: NodeODM → ScaleODM (quick guide)

Use this guide when you want the smallest possible code change to keep using pyodm with ScaleODM.

## What changes vs what stays the same

**Changes:** task creation. `node.create_task([...])` uploads local files, but ScaleODM expects images already in S3.

**Stays the same:** after you have a task UUID, pyodm `Task` methods still work for status polling, waiting, cancel/remove, and asset downloads.

## Minimal create-task migration

```python
# BEFORE (NodeODM)
from pyodm import Node

node = Node("nodeodm-host", 3000)
task = node.create_task(["img1.jpg", "img2.jpg"])
```

```python
# AFTER (ScaleODM)
import json
from pyodm import Node
from pyodm.api import Task

node = Node("scaleodm-host", 31100)

resp = node.post("/task/new", data={
    "name": "my-project",
    # Use either readS3Path or zipurl
    "readS3Path": "s3://mybucket/project/images/",
    # Optional but recommended for explicit output location
    "writeS3Path": "s3://mybucket/project/output/",
    # Optional: specify additional config options
    "options": json.dumps([
        {"name": "fast-orthophoto", "value": True}
    ]),
})

uuid = resp.json()["uuid"]
task = Task(node, uuid)
```

## Simplest status workflow

```python
info = task.info()
print(info.status, info.progress)

task.wait_for_completion()
```

## Simplest output workflow

```python
task.download_assets("/tmp/results/")
```

ScaleODM download endpoints use redirect-backed downloads (HTTP 302 to pre-signed S3 URLs). `download_assets(...)` handles this flow.

## Gotchas

- Input images must already exist in S3 before calling `/task/new`.
- If you pass `s3Endpoint` per task (for MinIO/Garage/etc.), it must be reachable from workflow pods, not only from your client machine.

## Need full endpoint/reference details?

See [NodeODM API compatibility reference](./nodeodm-compatibility.md).
