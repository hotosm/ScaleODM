<!-- markdownlint-disable -->
<div align="center">
    <h1>ScaleODM</h1>
    <p>Kubernetes-native auto-scaling and load balancing for OpenDroneMap.</p>
    <a href="https://github.com/hotosm/ScaleODM/releases">
        <img src="https://img.shields.io/github/v/release/hotosm/ScaleODM?logo=github" alt="Release Version" />
    </a>
</div>
<p align="center">
  <a href="https://github.com/hotosm/ScaleODM/actions/workflows/release_binary.yml" target="_blank">
      <img src="https://github.com/hotosm/ScaleODM/actions/workflows/release_binary.yml/badge.svg" alt="Binary">
  </a>
  <a href="https://github.com/hotosm/ScaleODM/actions/workflows/release_image.yml" target="_blank">
      <img src="https://github.com/hotosm/ScaleODM/actions/workflows/release_image.yml/badge.svg" alt="Image">
  </a>
  <a href="https://github.com/hotosm/ScaleODM/actions/workflows/release_chart.yml" target="_blank">
      <img src="https://github.com/hotosm/ScaleODM/actions/workflows/release_chart.yml/badge.svg" alt="Chart">
  </a>
  <a href="https://github.com/hotosm/ScaleODM/actions/workflows/test.yml" target="_blank">
      <img src="https://github.com/hotosm/ScaleODM/actions/workflows/test.yml/badge.svg" alt="Test">
  </a>
</p>

---

<!-- markdownlint-enable -->

## What Is ScaleODM?

ScaleODM is a Kubernetes-native orchestration layer for OpenDroneMap,
designed to automatically scale processing workloads using native
Kubernetes primitives such as Jobs, Deployments, and Horizontal Pod
Autoscalers.

It aims to provide the same API surface as NodeODM, while replacing
both NodeODM and ClusterODM with a single, cloud-native control plane.

> [!NOTE]
> ScaleODM has no authentication mechanisms, and should not be exposed
> publicly.
>
> Instead, your frontend connects to a backend. The backend then uses
> `PyODM` or similar to connect to the internal network ScaleODM
> instance.
>
> In order to federate multiple ScaleODM instances, a secure network
> mesh should be made with tools like Tailscale.

## Rationale

- ClusterODM --> NodeODM --> ODM are all fantastic tools, well tested
  with a big community behind them.
- However, running these tools inside a Kubernetes cluster poses a 
  few challenges:

#### ClusterODM Limitations

- Scaling relies on provisioning or deprovisioning *VMs*,
  not container replicas.
- Kubernetes-native scaling (Deployments, Jobs, KEDA)
  doesn't map neatly to its model.

#### NodeODM Limitations

- Data ingestion depends on `zip_url` or uploading via HTTP.
- S3 integration covers outputs only, not input data. Ideally
  we need a data 'pull' approach instead of data 'push'.
- Built-in file-based queues are not distributed or
  Kubernetes-aware.
  
### v1 Experiments

Our initial goal was to deploy ClusterODM and NodeODM *as-is* inside
Kubernetes, scaling NodeODM instances dynamically via [KEDA](https://keda.sh).

ScaleODM was introduced as a lightweight queueing API, backed by PostgreSQL
(`SKIP LOCKED`), acting as a mediator for job scheduling and scaling triggers.

However, two main challenges emerged:

1. **NodeODM's internal queueing** is file-based and not easily abstracted for
   distributed scaling.
2. **Data ingestion** still required either HTTP uploads or `zip_url` packaging,
   adding unnecessary I/O overhead.

NodeODM wasn't really designed for ephemeral or autoscaled container
environments, and that's fine.

### v2 Implementation

Rethinking the architecture: instead of orchestrating NodeODM instances, it
probably makes more sense to orchestrate ODM workloads inside as
**Kubernetes Jobs or Argo Workflows**.

Key concepts:
- **NodeODM-compatible API:** ScaleODM exposes the same REST endpoints as
  NodeODM, ensuring 🤞 compatibility with existing tools (e.g. PyODM).
- **Kubernetes Jobs:** Each processing task is executed in an ephemeral
  container, than can be distributed by the control plane as needed.
- **S3-native workflow:** Each job downloads inputs, performs processing,
  uploads outputs, and exits cleanly - no persistent volumes required.
  (i.e. jobs include the S3 params / credentials).
- **Federation:** ScaleODM instances can be federated across clusters,
  enabling global load balancing and community resource sharing.

The decision to take this approach **was not taken lightly**, as we are
strong supporters of contributing to existing open-source projects.

Long term, hopefully the ODM community can steward this project
as an alternative processing API (with different requirements).

For more details, see the [decisions](./decisions/README.md) section
in this repo.

## Roadmap

<!-- prettier-ignore-start -->

| Status | Feature | Release |
|:------:|:-------:|:--------|
| ✅ | NodeODM-compatible API (submit, status, download) | v1 |
| ✅ | Processing pipeline using Argo workflows + ODM containers | v1 |
| ✅ | Using the same job statuses as NodeODM (QUEUED, RUNNING, FAILED, COMPLETED, CANCELED) | v1 |
| ✅ | Env var config for API / pipeline | v1 |
| ✅ | Accept both zipped and unzipped imagery via S3 dir | v1 |
| ✅ | Progress monitoring via API by hooking into the ODM container logs | v1 |
| 🔄 | Pre-processing to determine the required resource usage for the workflow (CPU / RAM allocated) | v2 |
| 🔄 | Split-merge workflow | v2 |
| 📅 | Accept GCP as part of job submission | v2 |
| 📅 | Federation of ScaleODM instances and task distribution | v3 |
| 📅 | Webhook triggering - send a notification to external system when complete | v3 |
| 📅 | Post processing of the final artifacts - capability present in NodeODM | v4 |
| 📅 | Consider a load balancing service across all ScaleODM instances in DB | v4 |
| 📅 | Adding extra missing things from NodeODM implementation, if required* | v4 |

<!-- prettier-ignore-end -->

*missing NodeODM functionality
- Exposing all of the config options possible in ODM.
- Multi-step project creation endpoints, with direct file upload.
- Exposing all of the config options possible in ODM.

## Deployment

### Helm chart (OCI)

ScaleODM is distributed as an **OCI Helm chart** in the GitHub Container Registry:

- `oci://ghcr.io/hotosm/charts/scaleodm`

You can install it directly from the registry:

```bash
# (Optional) authenticate to GHCR if required by your environment
# echo "$GITHUB_TOKEN" | helm registry login ghcr.io --username "$GITHUB_ACTOR" --password-stdin

helm install scaleodm oci://ghcr.io/hotosm/charts/scaleodm \
  --version <chart-version> \
  --set database.external.enabled=true \
  --set database.external.secret.name="scaleodm-db-vars" \
  --set database.external.secret.key="SCALEODM_DATABASE_URL" \
  --set s3.external.secret.name="scaleodm-s3-vars" \
  --set argo.enabled=true
```

Replace `<chart-version>` with the desired chart version (e.g. the latest release tag).  
See `chart/README.md` for full configuration options and additional deployment scenarios.

### S3 Usage

ScaleODM supports two modes for S3 access:

#### Static Credentials (Simple, Less Secure)

- Set `SCALEODM_S3_ACCESS_KEY` and `SCALEODM_S3_SECRET_KEY` environment variables.
- These credentials are passed directly to all workflow jobs.
- **Note:** This is less secure as credentials are stored in the cluster.

#### STS Temporary Credentials (Recommended)

For better security, use AWS STS to generate temporary credentials per job:

1. **Set environment variables:**
   ```bash
   SCALEODM_S3_ACCESS_KEY=<your-iam-user-access-key>
   SCALEODM_S3_SECRET_KEY=<your-iam-user-secret-key>
   SCALEODM_S3_STS_ROLE_ARN=arn:aws:iam::ACCOUNT_ID:role/scaleodm-workflow-role
   SCALEODM_S3_STS_ENDPOINT=  # Optional: defaults to https://sts.{region}.amazonaws.com
   ```

2. **IAM User Permissions** (for the user specified in `SCALEODM_S3_ACCESS_KEY`):

   The IAM user must have permission to assume the STS role:
   ```json
   {
     "Version": "2012-10-17",
     "Statement": [
       {
         "Effect": "Allow",
         "Action": "sts:AssumeRole",
         "Resource": "arn:aws:iam::ACCOUNT_ID:role/scaleodm-workflow-role"
       }
     ]
   }
   ```

   **Important:** The `Resource` must match the exact role ARN specified in `SCALEODM_S3_STS_ROLE_ARN`. Using `"Resource": "*"` is less secure but allows assuming any role.

3. **IAM Role Trust Policy** (for the role specified in `SCALEODM_S3_STS_ROLE_ARN`):

   The role must trust the IAM user:
   ```json
   {
     "Version": "2012-10-17",
     "Statement": [
       {
         "Effect": "Allow",
         "Principal": {
           "AWS": "arn:aws:iam::ACCOUNT_ID:user/your-scaleodm-user"
         },
         "Action": "sts:AssumeRole"
       }
     ]
   }
   ```

4. **IAM Role Permissions** (for the role):

   The role must have permissions to read/write to your S3 buckets:
   ```json
   {
     "Version": "2012-10-17",
     "Statement": [
       {
         "Effect": "Allow",
         "Action": [
           "s3:GetObject",
           "s3:PutObject",
           "s3:DeleteObject",
           "s3:ListBucket"
         ],
         "Resource": [
           "arn:aws:s3:::your-bucket-name/*",
           "arn:aws:s3:::your-bucket-name"
         ]
       }
     ]
   }
   ```

**How it works:**
- When a job is submitted, ScaleODM uses the IAM user credentials to call `sts:AssumeRole` on the specified role.
- Temporary credentials (valid for 24 hours) are generated and injected into the workflow.
- Each workflow job uses these temporary credentials to access S3.
- Credentials automatically expire, reducing security risk.

**Troubleshooting:**

If you see errors like:
```
User: arn:aws:iam::ACCOUNT_ID:user/your-user is not authorized to perform: sts:AssumeRole on resource: arn:aws:iam::ACCOUNT_ID:role/your-role
```

Check:
1. The IAM user has `sts:AssumeRole` permission for the role ARN.
2. The role's trust policy allows the IAM user to assume it.
3. The `SCALEODM_S3_STS_ROLE_ARN` is set to a **role ARN** (not a user ARN).

## Development

- Binary and container image distribution is automated on new **release**.

### Local Development Setup

For local development and testing, ScaleODM uses a Talos Kubernetes cluster
created via `talosctl cluster create`. This provides a real Kubernetes
environment for testing Argo Workflows integration.

**Quick start:**

```bash
# Setup Talos cluster and start all services
just dev
```

This will:
1. Create a local Talos Kubernetes cluster
2. Install Argo Workflows
3. Start PostgreSQL, MinIO, and the ScaleODM API

**Manual setup:**

```bash
# 1. Setup Talos cluster (one-time)
just test cluster-init

# 2. Start compose services
just start
```

**Testing workflow:**

```bash
just test cluster-init     # Setup cluster
just test all              # Run tests
just test cluster-destroy  # Clean up
```

See [compose.README.md](./compose.README.md) for detailed setup instructions.

**Prerequisites:**
- `talosctl` installed ([installation guide](https://www.talos.dev/latest/introduction/install/))
- Docker running
- At least 8GB free memory

### Run The Tests

The test suite depends on a database and Kubernetes cluster:

```bash
# With Talos cluster already running
just test all

# Or manually
docker compose -f compose.yaml -f compose.test.yaml run --rm api go test -timeout=2m -v ./...
```
