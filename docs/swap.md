# Swap-based memory sizing

ScaleODM's image-count estimate is a brief **peak**, not the resident set. So it
requests steady-state RAM only and lets the kernel spill the peak to swap under
Kubernetes' `LimitedSwap` policy:

```
request (real RAM)     = peak / (1 + SWAP_RATIO)             # prod 1.0 -> ~half of peak
limit  (resident cap)  = request * (1 + MARGIN_PERCENT/100)  # swap on: < node RAM
limit  (OOM ceiling)   = peak    * (1 + MARGIN_PERCENT/100)  # swap off: whole peak as RAM
```

The limit is `memory.max`, which caps **resident** memory. With swap on it targets
below node RAM so the kernel is forced to push the peak out. Land it above node RAM
and the cgroup gets no pressure, resident fills the node, and kubelet's
**swap-blind** node-pressure eviction kills the pod with swap still free.
`request < limit` keeps the pod Burstable, which is what earns it a swap grant.
Guaranteed pods (`request == limit`) get none.

> [!WARNING]
> "Below node RAM" isn't guaranteed. Karpenter picks the node *after* sizing, so a
> request just under a node's allocatable leaves no room for the margin (request
> 113Gi on a 128Gi node gives a 136Gi limit). Node reclaim tuning below is the
> backstop. Watch jobs whose request sits near a node-size boundary.

kubelet grants swap **proportionally**, not the full `limit - request`:

```
pod swap = request / node_RAM * node_swap
```

So the node needs enough swap that this grant covers the peak, or the peak still
OOMs. Workflows carry `scaleodm.hotosm.org/burst-headroom-gib` (`limit - request`)
as a rough guide; the real allowance is `container_swap_limit_bytes`.

**Validated in production.** A 12k-image job pushed 344 GiB through swap while
pinned at `memory.max` for 27 minutes in OpenMVS densify, and completed. A later
run of the same imagery with a real `--boundary` never touched swap: the peak fell
below `memory.max` and node RAM absorbed it.

Rationale and headroom analysis:
[`decisions/0003-swap.md`](../decisions/0003-swap.md).

## Two layers

- **Node:** swap exists and kubelet allows it. Platform-specific, below.
- **Workload:** the pod sets `request < limit` (Burstable), so the kernel grants it
  swap. Platform-agnostic, so ScaleODM never needs to know how a node got its swap.

kubelet config is the same everywhere; only the node bootstrap differs:

```yaml
failSwapOn: false
memorySwap:
  swapBehavior: LimitedSwap
# featureGates: { NodeSwap: true }   # only on older kubelets where NodeSwap is beta
```

## AWS / Karpenter (default)

ScaleODM runs on its own dedicated, tainted pool so swap nodes aren't shared. In
`k8s-infra`:

- `apps/karpenter/scaleodm-nodepool.yaml`: Intel `*id` (local-NVMe) pool, labelled
  `pool=scaleodm`, tainted `dedicated=scaleodm:NoSchedule`.
- `apps/karpenter/scaleodm-ec2nodeclass.yaml`: `userData` puts a 2x-RAM swap file on
  the NVMe instance store (`instanceStorePolicy: RAID0`, ~100x faster than EBS),
  sets the kubelet flags above, and tunes the VM: `vm.swappiness=10` to keep the hot
  set in RAM outside the peak, plus `vm.min_free_kbytes` and
  `vm.watermark_scale_factor` so kswapd reclaims early and a fast fusion spike can't
  outrun it into eviction.

ScaleODM targets the pool with `nodeSelector=pool=scaleodm`,
`tolerations=dedicated=scaleodm:NoSchedule`, `schedulingMode=karpenter`.

**Fail-closed:** a kubelet drop-in makes kubelet `Requires=` the swap unit, so swap
is on before kubelet reads capacity. If swap setup fails the node never goes Ready
and Karpenter replaces it, rather than joining swapless and OOM-killing a
multi-hour job.

### Verify a node

A `Ready` node already implies swap succeeded, via that gate. To confirm:

```bash
# host swap: expect ~2x RAM on /mnt/k8s-disks/0/swapfile
swapon --show

# pod swap allowance. Target the worker container, NOT the default:
# kubectl exec defaults to a 2Gi sidecar. Containers: wait / download / process / upload.
# Check this FIRST on an eviction or OOM. Must be roughly request/node_RAM * node_swap,
# not just > 0. Zero means no grant and it will OOM at the peak.
kubectl -n odm exec <pod> -c process -- cat /sys/fs/cgroup/memory.swap.max
```

On a new dataset, watch the densify/fuse and mvs-texturing peaks:
`memory.swap.current` should climb, `memory.events` should show no OOM, node
`memory.available` should stay above 100Mi. Worst case is a *contained* cgroup OOM,
not a node-level eviction.

```bash
kubectl -n odm exec <pod> -c process -- grep -H . \
  /sys/fs/cgroup/memory.current /sys/fs/cgroup/memory.peak \
  /sys/fs/cgroup/memory.swap.current /sys/fs/cgroup/memory.swap.peak \
  /sys/fs/cgroup/memory.swap.max /sys/fs/cgroup/memory.events \
  /sys/fs/cgroup/memory.swap.events
```

## Generic / self-managed (Hetzner, kubeadm, k3s, bare metal)

> [!NOTE]
> With a huge-RAM machine, skip swap entirely.

Switching `schedulingMode` isn't enough. Nodes must support swap and pods must be
pinned to them, or a reduced request just OOMs. Needs cgroups v2, a swap-capable
kubelet, and ideally a fast, encrypted, dedicated swap disk.

1. Create and persist swap on each node:

   ```bash
   sudo fallocate -l 128G /swapfile   # ~2x RAM
   sudo chmod 600 /swapfile
   sudo mkswap /swapfile
   sudo swapon /swapfile
   echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
   ```

2. Add the kubelet config above to `/var/lib/kubelet/config.yaml` (or a k3s
   kubelet-arg drop-in), restart kubelet, label the nodes
   (`kubectl label node <n> pool=swap`).

3. Point ScaleODM at them. The selector is **required**, or pods OOM on non-swap
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
| `SCALEODM_PROCESS_MEMORY_LIMIT_MARGIN_PERCENT` | `20` | limit margin. Swap off: above the peak. Swap on: above the request |
| `SCALEODM_PROCESS_CPU_PER_GIB` / `processSizing.cpuPerGiB` | `0.125` | CPU request per GiB of RAM request (0.125 = r-family) |
| `SCALEODM_PROCESS_CPU_LIMIT_MULTIPLIER` / `processSizing.cpuLimitMultiplier` | `1.5` (prod `0`) | CPU limit = request x this; `0` omits it so ODM bursts to every core |
| `SCALEODM_WORKFLOW_ONDEMAND_IMAGE_THRESHOLD` / `config.workflow.onDemandImageThreshold` | `5000` | auto-upgrade spot to on-demand at/above this count; `0` disables |
| `SCALEODM_WORKFLOW_SCHEDULING_MODE` / `config.workflow.schedulingMode` | `karpenter` | `karpenter` or `generic` |
| `SCALEODM_WORKFLOW_NODE_SELECTOR` / `config.workflow.nodeSelector` | `node-type=cpu` | base node selector |
| `SCALEODM_WORKFLOW_TOLERATIONS` / `config.workflow.tolerations` | `""` | extra tolerations |
