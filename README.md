# ScaleODM

<!-- markdownlint-disable -->
<p align="center">
  <em>Kubernetes-native auto-scaling and load balancing for OpenDroneMap.</em>
</p>
<p align="center">
  <a href="https://github.com/hotosm/ScaleODM/actions/workflows/release.yml" target="_blank">
      <img src="https://github.com/hotosm/ScaleODM/actions/workflows/release.yml/badge.svg" alt="Build & Release">
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

## Roadmap

<!-- prettier-ignore-start -->

| Status | Feature | Release |
|:------:|:-------:|:--------|
| 🔄 | NodeODM-compatible API (submit, status, download) | v1 |
| 🔄 | Using the same job statuses as NodeODM (QUEUED, RUNNING, FAILED, COMPLETED, CANCELED) | v1 |
| 📅 | Accept GCP as part of job submission | v1 |
| 📅 | Federation of ScaleODM instances and task distribution | v2 |
| 📅 | Progress monitoring via API by hooking into the ODM container logs | v2 |
| 📅 | Webhook triggering - send a notification to external system when complete | v2 |
| 📅 | Post processing of the final artifacts - capability present in NodeODM | v3 |
| 📅 | Consider a load balancing service across all ScaleODM instances in DB | v4 |
| 📅 | Adding extra missing things from NodeODM implementation, if required* | v4 |

<!-- prettier-ignore-end -->

*missing NodeODM functionality
- Exposing all of the config options possible in ODM.
- Multi-step project creation endpoints, with direct file upload.
- Exposing all of the config options possible in ODM.

## Usage

Details to come once API is stabilised.

### S3 Usage

- Setting the access / secret key will pass these credentials to all
  jobs. This isn't the most secure setup.
- A better option is to set the `SCALEODM_S3_STS_ROLE_ARN` variable too.
- The user for the access/secret key pair must have permission to
  generate temporary security credentials.
- The role of the provided ARN will be assumed for the temp creds
  (24hr access), so be sure this user has permission to read/write
  to the required S3 buckets with imagery.

  Allowing to assume any role (*) is less secure than specifying
  an exact role that can be assumed, but more restrictive:
  ```json
  {
    "Version": "2012-10-17",
    "Statement": [
      {
        "Effect": "Allow",
        "Action": "sts:AssumeRole",
        "Resource": "*"
      }
    ]
  }
  ```

## Development

- Binary and container image distribution is automated on new **release**.

### Run The Tests

The test suite depends on a database, so the most convenient way is to run
via docker.

There is a pre-configured `compose.yml` for testing:

```bash
docker compose run --rm scaleodm
```
