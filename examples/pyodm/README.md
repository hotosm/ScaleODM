# pyodm Example

Uses the [pyodm](https://github.com/OpenDroneMap/PyODM) SDK to create and monitor ScaleODM tasks.

- Ensure you have a `.env` file in the repo root.
- See `example-pyodm` in the Justfile for the example command.
- For local S3-compatible providers (Garage/MinIO), set `SCALEODM_WORKFLOW_S3_ENDPOINT`
  to an endpoint reachable by workflow pods (for example `http://host.docker.internal:31102`
  in local Docker/Talos setups).
- `AWS_S3_ENDPOINT` remains the API container's own S3 endpoint (typically
  `http://localhost:31102` in this repo's compose setup).
- ScaleODM normalizes endpoint values and uses path-style bucket addressing for compatibility.
