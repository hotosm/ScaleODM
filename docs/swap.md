# Swap-based memory sizing

ScaleODM's image-count estimate is a brief **peak**, not the resident set. So it
requests only steady-state RAM and lets the kernel spill the peak to swap under
Kubernetes' `LimitedSwap` policy:

```
request (real RAM)     = peak / (1 + SWAP_RATIO)             # prod 1.0 -> ~half of peak
limit  (resident cap)  = request * (1 + MARGIN_PERCENT/100)  # swap on: < node RAM
limit  (OOM ceiling)   = peak    * (1 + MARGIN_PERCENT/100)  # swap off: whole peak as RAM
```

The limit is the container's `memory.max`, which caps **resident** (non-swapped)
memory. With swap on it targets **below node RAM** so the kernel is forced to push
the peak into swap. If it lands above node RAM the cgroup gets no pressure, resident
fills the node, and kubelet's **swap-blind** node-pressure eviction (it ignores
swap) kills the pod even with swap free. `request < limit` keeps the pod
Burstable so it gets a swap grant - Guaranteed pods (`request == limit`) get none.

> [!WARNING]
> "Below node RAM" is not guaranteed: Karpenter picks the node *after* sizing, so a
> request just under a node's allocatable leaves no room for the margin (e.g.
> request 113Gi on a 128Gi node -> limit 136Gi). Node reclaim tuning (below) is the
> backstop; watch jobs whose request sits near a node-size boundary.

Rationale and headroom analysis: [`decisions/0003-swap.md`](../decisions/0003-swap.md).

## Two layers

- **Node:** swap exists and kubelet allows it. Platform-specific (below).
- **Workload:** the pod sets `request < limit` (Burstable QoS), so the kernel
  grants it swap. Platform-agnostic - ScaleODM never needs to know how a node got
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
  than EBS), sets the kubelet flags above, and tunes the VM (initial values,
  pending validation on a real large job):
  `vm.swappiness=10` (swap is only needed for the brief peak, so keep the hot set
  in RAM the rest of the time - second-order; the container limit forces the peak
  into swap), plus `vm.min_free_kbytes` + `vm.watermark_scale_factor` so kswapd
  reclaims early and a fast fusion spike can't outrun it into eviction.

**Fail-closed:** a kubelet drop-in makes kubelet `Requires=` the swap unit, so
swap is on *before* kubelet reads capacity, and if swap setup fails the node
never goes Ready - Karpenter replaces it instead of silently joining swapless and
OOM-killing a multi-hour job.

ScaleODM targets the pool with `nodeSelector=pool=scaleodm`,
`tolerations=dedicated=scaleodm:NoSchedule`, and `schedulingMode=karpenter`.

### Verify a node

A `Ready` node already implies swap succeeded (the fail-closed gate). To confirm:

```bash
# host swap: expect ~2x RAM on /mnt/k8s-disks/0/swapfile
swapon --show

# pod swap allowance - target the worker container, NOT the default:
# kubectl exec defaults to a 2Gi sidecar. Containers: wait / download / process / upload.
# Check this FIRST when a job is evicted or OOM-killed. Must be roughly
# request/node_RAM * node_swap (not just > 0); 0 = no grant, will OOM at the peak.
kubectl -n odm exec <pod> -c process -- \
  cat /sys/fs/cgroup/memory.swap.max
```

The sizing is **not yet validated in production**: the limit caps resident memory below
node RAM so the peak spills to swap, but whether the job completes depends on how
much of fusion's working set is swappable. Worst case is a *contained* cgroup OOM
(not a node-level eviction). So watch a real large job at the OpenMVS densify/fuse
and mvs-texturing peaks: `memory.swap.current` should climb, `memory.events` should
show no OOM, and node `memory.available` should stay above 100Mi.

```bash
kubectl -n odm exec <pod> -c process -- grep -H . \
  /sys/fs/cgroup/memory.current /sys/fs/cgroup/memory.peak \
  /sys/fs/cgroup/memory.swap.current /sys/fs/cgroup/memory.swap.peak \
  /sys/fs/cgroup/memory.swap.max /sys/fs/cgroup/memory.events \
  /sys/fs/cgroup/memory.swap.events
```

## Generic / self-managed (Hetzner, kubeadm, k3s, bare metal)

> [!NOTE]
> With a huge-RAM machine you can skip swap entirely.

Switching `schedulingMode` isn't enough - nodes must support swap and pods must
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

3. Point ScaleODM at them - the selector is **required**, or pods OOM on non-swap
   nodes:

   ```
   SCALEODM_WORKFLOW_SCHEDULING_MODE=generic
   SCALEODM_WORKFLOW_NODE_SELECTOR=pool=swap
   SCALEODM_WORKFLOW_TOLERATIONS=              # e.g. swap=true:NoSchedule if tainted
   ```

## Options

| Env / Helm value | Default | Purpose |
|---|---|---|
| `SCALEODM_PROCESS_SWAP_RATIO` / `processSizing.swapRatio` | `0` (prod `1.0`) | `request = peak/(1+ratio)`; `0` disables. Only set > 0 with a swap-node selector |
| `SCALEODM_PROCESS_MEMORY_REQUEST_MIN_GIB` / `processSizing.memoryRequestMinGiB` | `4` | floor for the RAM request |
| `SCALEODM_PROCESS_MEMORY_LIMIT_MARGIN_PERCENT` | `20` | limit margin. Swap off: above the peak. Swap on: above the request (keeps the limit below node RAM and the pod Burstable) |
| `SCALEODM_PROCESS_CPU_PER_GIB` / `processSizing.cpuPerGiB` | `0.125` | CPU request per GiB of RAM request (0.125 = r-family) |
| `SCALEODM_PROCESS_CPU_LIMIT_MULTIPLIER` / `processSizing.cpuLimitMultiplier` | `1.5` (prod `0`) | CPU limit = request x this; `0` omits the limit so ODM bursts to every node core |
| `SCALEODM_WORKFLOW_ONDEMAND_IMAGE_THRESHOLD` / `config.workflow.onDemandImageThreshold` | `5000` | auto-upgrade spot to on-demand at/above this image count; `0` disables |
| `SCALEODM_WORKFLOW_SCHEDULING_MODE` / `config.workflow.schedulingMode` | `karpenter` | `karpenter` or `generic` |
| `SCALEODM_WORKFLOW_NODE_SELECTOR` / `config.workflow.nodeSelector` | `node-type=cpu` | base node selector |
| `SCALEODM_WORKFLOW_TOLERATIONS` / `config.workflow.tolerations` | `""` | extra tolerations |
