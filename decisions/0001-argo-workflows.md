# Use Kubernetes-Native Argo Workflows Instead of NodeODM + ClusterODM

## Context and Problem Statement

ScaleODM's goal is to provide a cloud-native, auto-scaling orchestration layer for
OpenDroneMap (ODM). The existing OpenDroneMap ecosystem provides two key tools:

- **NodeODM:** Runs individual ODM jobs as a REST API service.
- **ClusterODM:** Distributes jobs across multiple NodeODM instances.

While both are robust and widely adopted, they were not designed for Kubernetes-native
environments. Running NodeODM and ClusterODM inside Kubernetes introduced several
challenges:

- ClusterODM scales *VMs*, not *containers*, and doesn't leverage Kubernetes Jobs,
  Deployments, or Autoscalers.
- NodeODM's queueing system is file-based and not distributed.
- Data ingestion depends on uploading imagery via HTTP or providing a `zip_url`,
  adding I/O overhead and making it harder to integrate with S3.
- The NodeODM container model (long-running servers with stateful projects) doesn't
  fit Kubernetes' ephemeral job model.

We need a system that can:
- Orchestrate ODM workloads as ephemeral, stateless jobs.
- Scale horizontally using native Kubernetes primitives.
- Integrate cleanly with S3 for both input and output data.
- Maintain API compatibility with NodeODM for client interoperability.

## Considered Options

- Continue using **ClusterODM + NodeODM** inside Kubernetes.
- Wrap NodeODM with a custom queue and scaling layer (e.g. PostgreSQL-based).
- Replace NodeODM/ClusterODM orchestration with **Kubernetes-native Argo Workflows**.

## Decision Outcome

We chose to replace NodeODM + ClusterODM orchestration with **Kubernetes-native Argo
Workflows**.

Each ODM processing task is defined as a workflow that runs:
1. A download step to sync imagery from S3.
2. A processing step using the standard ODM container.
3. An upload step to push final artifacts back to S3.

This workflow model maps naturally to Kubernetes' scheduling, scaling, and failure
recovery mechanisms. It also simplifies the system by removing the need for custom
queueing, stateful NodeODM instances, or VM-level scaling.

Argo Workflows natively supports:
- Long-running, resumable, and observable pipelines.
- Native retry, TTL cleanup, and artifact management.
- Declarative YAML specifications compatible with CI/CD tooling.
- Native integration with Kubernetes Secrets, Volumes, and ServiceAccounts.

### Consequences

- ✅ processing jobs are now stateless, isolated, and restartable.
- ✅ scaling is handled entirely by Kubernetes (HPA, KEDA, etc.).
- ✅ S3 becomes the single source of truth for inputs and outputs.
- ✅ NodeODM-compatible API means existing tooling like pyODM should work.
- ✅ federation (multi-cluster job delegation) becomes straightforward.
- ❌ Argo Workflows introduces additional CRD complexity and controller overhead.
- ❌ ScaleODM becomes entirely dependent on Kubernetes and installing Argo
  workflows into the cluster environment.
