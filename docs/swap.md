# Swap-based memory sizing

ScaleODM's image-count estimate is a brief **peak**, not the resident set. So it
requests only steady-state RAM and lets the kernel spill the peak to swap under
Kubernetes' `LimitedSwap` policy:

```
request (real RAM)    = peak / (1 + SWAP_RATIO)          # prod 1.0 -> ~half of peak
limit   (OOM ceiling) = peak * (1 + MARGIN_PERCENT/100)  # backed by RAM + swap
```

Rationale and headroom analysis: [`decisions/0003-swap.md`](../decisions/0003-swap.md).

## Two layers

- **Node:** swap exists and kubelet allows it. Platform-specific (below).
- **Workload:** the pod sets `request < limit` (Burstable QoS), so the kernel
  grants it swap. Platform-agnostic — ScaleODM never needs to know how a node got
  its swap.

kubelet grants swap **proportionally**, not the full `limit - request`:

```
pod swap = request / node_RAM * node_swap
```

So a node needs enough swap that this grant covers the peak, or a peak still
OOM-kills. Each workflow is annotated with `scaleodm.hotosm.org/burst-headroom-gib`
(`limit - request`) as a rough guide; the real allowance is
`container_swap_limit_bytes`.

## kubelet config (same everywhere)

```yaml
failSwapOn: false
memorySwap:
  swapBehavior: LimitedSwap
# featureGates: { NodeSwap: true }   # only on older kubelets where NodeSwap is still beta
```

Only the node bootstrap differs.

## AWS / Karpenter (default)

ScaleODM runs on its own dedicated, tainted pool so swap nodes aren't shared. In
`k8s-infra`:

- `apps/karpenter/scaleodm-nodepool.yaml`: Intel `*id` (local-NVMe) pool,
  labelled `pool=scaleodm`, tainted `dedicated=scaleodm:NoSchedule`.
- `apps/karpenter/scaleodm-ec2nodeclass.yaml`: `userData` creates a 2x-RAM swap
  file on the NVMe instance store (`instanceStorePolicy: RAID0`, ~100x faster
  than EBS) and sets the kubelet flags above.

**Fail-closed:** a kubelet drop-in makes kubelet `Requires=` the swap unit, so
swap is on *before* kubelet reads capacity, and if swap setup fails the node
never goes Ready — Karpenter replaces it instead of silently joining swapless and
OOM-killing a multi-hour job.

ScaleODM targets the pool with `nodeSelector=pool=scaleodm`,
`tolerations=dedicated=scaleodm:NoSchedule`, and `schedulingMode=karpenter`.

### Verify a node

A `Ready` node already implies swap succeeded (the fail-closed gate). To confirm:

```bash
# host swap: expect ~2x RAM on /mnt/k8s-disks/0/swapfile
swapon --show

# pod swap allowance — target the worker container, NOT the default:
# kubectl exec defaults to a 2Gi sidecar. Containers: wait / download / process / upload.
kubectl -n odm exec <pod> -c process -- \
  cat /sys/fs/cgroup/memory.swap.max       # ~= peak GiB (proportional to request)
```

Swap only fills once RSS nears node RAM — i.e. the OpenMVS densify/fuse stage.
Watch it prove out there (climbs instead of OOMKill), then record the peak:

```bash
kubectl -n odm exec <pod> -c process -- \
  sh -c 'grep . /sys/fs/cgroup/memory.swap.current /sys/fs/cgroup/memory.swap.peak'
```

## Generic / self-managed (Hetzner, kubeadm, k3s, bare metal)

> [!NOTE]
> With a huge-RAM machine you can skip swap entirely.

Switching `schedulingMode` isn't enough — nodes must support swap and pods must
be pinned to them, or a reduced request just OOMs. Needs cgroups v2, a
swap-capable kubelet, and ideally a fast, encrypted, dedicated swap disk (swap
persists page contents to disk).

1. Create + persist swap on each node:

   ```bash
   sudo fallocate -l 128G /swapfile   # ~2x RAM
   sudo chmod 600 /swapfile
   sudo mkswap /swapfile
   sudo swapon /swapfile
   echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
   ```

2. Add the kubelet config above to `/var/lib/kubelet/config.yaml` (or a k3s
   kubelet-arg drop-in), restart kubelet, and label the nodes
   (`kubectl label node <n> pool=swap`).

3. Point ScaleODM at them — the selector is **required**, or pods OOM on non-swap
   nodes:

   ```
   SCALEODM_WORKFLOW_SCHEDULING_MODE=generic
   SCALEODM_WORKFLOW_NODE_SELECTOR=pool=swap
   SCALEODM_WORKFLOW_TOLERATIONS=              # e.g. swap=true:NoSchedule if tainted
   ```

## Options

| Env / Helm value | Default | Purpose |
|---|---|---|
| `SCALEODM_PROCESS_SWAP_RATIO` / `processSizing.swapRatio` | `0` (prod `1.0`) | `request = peak/(1+ratio)`; `0` disables. Only set > 0 with a swap-node selector. Prod ran `2.0` until a node-pressure eviction (see `decisions/0003-swap.md`) showed the resident set exceeds peak/3 |
| `SCALEODM_PROCESS_MEMORY_REQUEST_MIN_GIB` / `processSizing.memoryRequestMinGiB` | `4` | floor for the RAM request |
| `SCALEODM_PROCESS_MEMORY_LIMIT_MARGIN_PERCENT` | `20` | headroom above the peak for the limit |
| `SCALEODM_PROCESS_CPU_PER_GIB` / `processSizing.cpuPerGiB` | `0.125` | CPU request per GiB of RAM request (0.125 = r-family) |
| `SCALEODM_PROCESS_CPU_LIMIT_MULTIPLIER` / `processSizing.cpuLimitMultiplier` | `1.5` (prod `0`) | CPU limit = request x this; `0` omits the limit so ODM bursts to every node core |
| `SCALEODM_WORKFLOW_ONDEMAND_IMAGE_THRESHOLD` / `config.workflow.onDemandImageThreshold` | `5000` | auto-upgrade spot to on-demand at/above this image count; `0` disables |
| `SCALEODM_WORKFLOW_SCHEDULING_MODE` / `config.workflow.schedulingMode` | `karpenter` | `karpenter` or `generic` |
| `SCALEODM_WORKFLOW_NODE_SELECTOR` / `config.workflow.nodeSelector` | `node-type=cpu` | base node selector |
| `SCALEODM_WORKFLOW_TOLERATIONS` / `config.workflow.tolerations` | `""` | extra tolerations |
