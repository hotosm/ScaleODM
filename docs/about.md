# About ScaleODM

ScaleODM exists to run ODM processing in Kubernetes-native environments without requiring the traditional NodeODM + ClusterODM control model. The core tension is straightforward: ODM tooling is mature and widely used, but the operational model of long-lived nodes and VM-centric scaling does not map cleanly to modern cluster workloads that prefer ephemeral jobs, declarative orchestration, and cloud object storage.

## Why this project exists

ClusterODM, NodeODM, and ODM are all strong projects with proven production use. ScaleODM is not a replacement born from feature dissatisfaction; it is a response to deployment constraints that appear when teams need autoscaling, workload isolation, and cloud-native operations.

In Kubernetes contexts, teams commonly need:

- per-task isolation with ephemeral compute,
- queueing and scheduling semantics that are cluster-aware,
- input/output patterns aligned to object storage instead of API uploads,
- horizontal scaling driven by native platform primitives.

## Constraints observed with existing patterns

### ClusterODM in Kubernetes

ClusterODM scales by managing machine/node style capacity. That paradigm is practical for VM pools, but it is less aligned with Kubernetes-native scaling strategies built around Jobs, controllers, and autoscaler policies.

### NodeODM in Kubernetes

NodeODM is designed around direct upload (`create_task`) and related chunked flows, while output S3 support is only one side of the data lifecycle. For cluster operations, this introduces friction when imagery already lives in object storage and processing should pull from S3 directly.

## v1 to v2 evolution

### v1 experiments

The first iteration kept NodeODM/ClusterODM expectations and introduced ScaleODM mainly as a queueing mediator (PostgreSQL with `SKIP LOCKED`) plus scaling glue. That validated some operational patterns, but two constraints remained:

1. NodeODM queueing and lifecycle assumptions are not ideal for highly ephemeral distributed workers.
2. Input ingestion still centered on uploads or archive URLs, creating avoidable I/O and orchestration overhead.

### v2 direction

The architecture shifted from orchestrating NodeODM instances to orchestrating ODM workloads directly as Kubernetes Jobs/Argo Workflows. In this model, each task is an isolated workflow that:

1. reads imagery from S3,
2. processes with ODM,
3. writes artifacts back to S3,
4. exposes task state through a NodeODM-compatible API surface.

This preserves familiar client interactions (including pyodm task monitoring/download methods) while adopting infrastructure patterns that scale more naturally in Kubernetes.

## Decision records

For technical ADRs and architectural context, see [decisions/README.md](../decisions/README.md).
