# Swap-based memory sizing

Setup and tuning for ScaleODM's swap-based sizing. For the rationale and the
OOM-headroom analysis, see [`decisions/0003-swap.md`](../decisions/0003-swap.md).

In short: the image-count estimate is a brief **peak**, not the resident set. So
ScaleODM requests only the steady-state RAM and lets the kernel spill the peak to
swap under Kubernetes' `LimitedSwap` policy:

```
request (real RAM)   = peak / (1 + SWAP_RATIO)          # prod 1.0, so ~1/2 of peak
limit   (OOM ceiling) = peak * (1 + MARGIN_PERCENT/100)  # backed by RAM + swap
```

## Two layers

Swap has two independent layers. Keeping them separate is what makes ScaleODM
portable across AWS and any other cluster.

- **Node layer:** swap exists on the node and kubelet allows it. Set up
  per-platform (below).
- **Workload layer:** the pod declares `request < limit` (Burstable QoS) and the
  kernel grants it swap. This is platform-agnostic; ScaleODM never needs to know
  how a node got its swap.

Each workflow is annotated with `scaleodm.hotosm.org/burst-headroom-gib`
(`limit - request`) so you can check nodes carry enough swap. Note this is the
memory the pod may use above its request, not the actual LimitedSwap grant (which
is proportional); watch `container_swap_limit_bytes` for the real allowance.

> Each node must provide **swap >= (limit - request)** for the largest pod it
> runs, or a peak still OOM-kills. Note kubelet grants swap *proportionally*
> (`swap = request/node_RAM * node_swap`), not the full `limit - request`; see
> the headroom section of `decisions/0003-swap.md`.

## Enabling swap on nodes

The kubelet config is identical everywhere:

```yaml
failSwapOn: false
featureGates:
  NodeSwap: true
memorySwap:
  swapBehavior: LimitedSwap
```

Only the bootstrap differs.

### AWS / Karpenter (default)

ScaleODM runs on its own dedicated, tainted node pool so the NVMe/swap nodes
aren't used by unrelated workloads. In `k8s-infra`:

- `apps/karpenter/scaleodm-nodepool.yaml`: Intel `*id` pool, labelled
  `pool=scaleodm`, tainted `dedicated=scaleodm:NoSchedule`.
- `apps/karpenter/scaleodm-ec2nodeclass.yaml`: `userData` installs a systemd
  unit that creates a 2x-RAM swap file on the local NVMe instance store
  (`instanceStorePolicy: RAID0`), and a `NodeConfig` sets the kubelet flags.
  NVMe swap is ~100x faster than EBS.

ScaleODM targets it with `nodeSelector=pool=scaleodm` and
`tolerations=dedicated=scaleodm:NoSchedule`, plus `schedulingMode=karpenter`
(default) which adds the capacity-type selector and spot toleration.

### Generic / self-managed (Hetzner, kubeadm, k3s, bare metal)

> [!NOTE]
> If you have a machine with a huge amount of RAM, then you can probably save the
> hassle for optimising with swap.

Switching `schedulingMode` alone is not enough. The nodes must genuinely support
swap, and pods must be pinned to those nodes, or a reduced RAM request just OOMs.
Requirements: cgroups v2 (LimitedSwap needs it), a kubelet built with swap
support, and ideally a fast, encrypted, dedicated swap disk (swap persists page
contents to disk, so encrypt it for any sensitive imagery).

1. Create swap on each node and persist it. Prefer a dedicated fast disk;
   encrypt it if the imagery is sensitive:

   ```bash
   sudo fallocate -l 128G /swapfile   # ~2x RAM
   sudo chmod 600 /swapfile
   sudo mkswap /swapfile
   sudo swapon /swapfile
   echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
   ```

2. Add the four kubelet settings above to `/var/lib/kubelet/config.yaml` (or the
   k3s kubelet-arg drop-in) and restart kubelet. Label the swap nodes, e.g.
   `kubectl label node <n> pool=swap`.

3. Switch ScaleODM to generic scheduling and **require** the swap-node label, so
   pods can't land on a non-swap node and OOM:

   ```
   SCALEODM_WORKFLOW_SCHEDULING_MODE=generic
   SCALEODM_WORKFLOW_NODE_SELECTOR=pool=swap   # required; must match your swap nodes
   SCALEODM_WORKFLOW_TOLERATIONS=              # e.g. swap=true:NoSchedule if tainted
   ```

   In Helm: `config.workflow.schedulingMode`, `.nodeSelector`, `.tolerations`.
   Leaving the default `node-type=cpu` selector on a non-Karpenter cluster
   leaves pods pending, so always set it to your own swap-node label.

The same ScaleODM build and pod spec run on both. Only the node bootstrap and
the scheduling labels/taints differ.

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
