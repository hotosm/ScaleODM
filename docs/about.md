# About ScaleODM

ScaleODM runs OpenDroneMap (ODM) processing on Kubernetes.

It is built for clusters that expect ephemeral jobs, declarative orchestration,
and object storage. The existing ODM tools are excellent, but they were not
designed with this in mind. This page explains where they struggle at scale, and
why we took a different approach.

## The existing tools

ODM already has two tools for running jobs at scale:

- **NodeODM** runs a single ODM job behind a REST API.
- **ClusterODM** spreads jobs across several NodeODM instances.

Both are robust and widely used, and ODM itself is mature and well proven. None
of what follows is a criticism of the projects. The problem is only their
deployment model, which does not fit Kubernetes well.

## Why NodeODM struggles to scale on Kubernetes

There are four main issues.

### 1. ClusterODM scales machines, not containers

ClusterODM scales by adding and removing whole machines (VMs). This works well
for a pool of virtual machines.

Kubernetes scales differently. It thinks in terms of Jobs, Deployments, and
autoscalers such as the HPA, KEDA, or Karpenter. ClusterODM uses none of these,
so you lose the native scaling that Kubernetes is good at.

### 2. NodeODM is a long-running, stateful server

A NodeODM container is a server that stays running and keeps each project on
local disk.

Kubernetes prefers pods that are ephemeral and stateless. They start, do one
job, then go away. A long-lived server that holds state on local disk fights
this model. It makes scaling down, and moving work between nodes, awkward.

### 3. The NodeODM queue is local, not distributed

NodeODM queues jobs using local files on a single instance. There is no shared
queue across the cluster.

Once you want to run many jobs across many nodes, a local file-based queue is not
enough to coordinate them.

### 4. Getting imagery in means an upload

NodeODM expects you to upload imagery over HTTP, or to pass a `zip_url` for it to
download.

In our case the imagery already lives in S3. Pushing it back through an upload
API is extra I/O and extra friction for no benefit. S3 is only supported for
output too, so the input path still has to run through the API.

## The ScaleODM approach

ScaleODM drops the NodeODM and ClusterODM control plane. Instead it runs each ODM
task directly as an [Argo Workflow](https://github.com/argoproj/argo-workflows)
on Kubernetes.

Every task is one isolated workflow with three steps:

1. Download the imagery from S3.
2. Process it with the standard ODM container.
3. Upload the results back to S3.

This maps cleanly onto how Kubernetes already works:

- Jobs are stateless, isolated, and can be restarted if they fail.
- Scaling, scheduling, retries, and cleanup are all handled by Kubernetes and
  Argo. We do not maintain our own queue or scaling glue.
- S3 is the single source of truth for both input and output.

Importantly, ScaleODM still exposes a **NodeODM-compatible API**. Existing tools
keep working, including pyodm's task monitoring and download methods. So you get
Kubernetes-native scaling without rewriting your clients.

The main trade-off is that ScaleODM depends fully on Kubernetes, and on Argo
Workflows being installed in the cluster. For our use, that is a fair price for
scaling that just works.

## A note on history (v1 to v2)

The first version of ScaleODM kept NodeODM and ClusterODM, and added a queueing
layer on top (PostgreSQL with `SKIP LOCKED`) plus some scaling glue.

It proved the queueing idea worked, but two problems remained. NodeODM's
long-lived, stateful model still did not suit ephemeral workers, and getting
imagery in still meant uploads or archive URLs.

v2 is the current approach described above: Argo Workflows, S3 for input and
output, and a NodeODM-compatible API on top.

## Decision records

For the full technical reasoning and architecture decisions, see the
[decision records](../decisions/README.md).
