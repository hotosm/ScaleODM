# Memory sizing

How much RAM (and swap) an ODM run needs, scaled by image count. For how
ScaleODM turns these numbers into pod requests/limits, see
[`swap.md`](./swap.md) and [`decisions/0003-swap.md`](../decisions/0003-swap.md).

## The short version

- RAM demand is a brief **peak**, not sustained across the workflow.
- The table below is total RAM **+ swap**. Provision less real RAM and let swap
  cover the peaks. With ~2x swap, a 62Gi machine handles ~5000 images.
- Past ~5000 images the peak lands in OpenMVS `DensifyPointCloud` and grows
  faster than linearly. It scales with **view count**, not depth-map resolution.

## Scaling estimate

| Images | RAM + swap |
|-------:|-----------:|
| 40     | 4 GB       |
| 250    | 16 GB      |
| 500    | 32 GB      |
| 1500   | 64 GB      |
| 2500   | 128 GB     |
| 3500   | 192 GB     |
| 5000   | 256 GB     |
| 12000  | 800 GB     |

Don't go much above 12000 images in a single scene. Past that, pass ODM's
`--split` / `--split-overlap` and let ODM build submodels internally, then merge
them. Note this still runs in one pod, with no fan out across workers.

## Observed datapoints

64 GB RAM machine (12 core), no swap: ~4000 image ceiling. With ~2x swap
(127 GB):

- Comfortable to ~1600. Swap nearly exhausted at 1869.
- 2658 images: out of swap, OOM-killed in OpenMVS, recovers and finishes.
- 4778 images: OOM-killed in OpenMVS, then again in mvs-texturing, and dies.
- ~144 GB peak for 3000 images, so ~4000 per submodel is the practical limit.
  Freetown WJ (34k images) used `--split 8000` to stay inside that.

11972 images, ODX 3.8.3 (OpenSfM v1), `--sfm-algorithm triangulation`, on a
384 GiB node with 743 GiB of swap (`memory.max` 360 GiB):

- Sparse SfM plus undistort took 8h20m at a ~192 GiB plateau. ODM 3.6.1 on the
  default incremental SfM never finished it in 48h.
- `pc-quality medium`: ~100 GiB steady, then one transient over 932 GiB in
  depth-map estimation. Blew through resident + swap and the pod was killed.
- `pc-quality low`: same shape, 709 GiB transient. Survived, but sat 27 minutes
  pinned at `memory.max` pushing 344 GiB through swap.
- Quartering the depth-map pixels only moved that peak ~25%, so `pc-quality`
  isn't the right flag to optimise on. Size the node instead.
- Fusion 67 GiB, filtering 18 GiB, meshing 45 GiB. Texturing hits 337 GiB but
  it's page cache, so swap stays flat.
- `odm_dem` + `odm_orthophoto` then pinned `memory.max` for **1h52m** and pushed
  419 GiB of swap — longer and larger than the densify transient. Most of that
  was wasted: a broken reconstruction inflated the ortho canvas to 77 Gpixels of
  which 92% was cropped away. Rendering cost tracks reconstruction *extent*, so
  bad geometry is also a sizing problem: pass a real `--boundary`, and avoid the
  flags listed in [`testing-alternative-images.md`](./testing-alternative-images.md).

## Notes

- Prod sizes 12k jobs onto an `r6id.24xlarge` (96 vCPU, 768 GiB, 1.5 TiB NVMe
  swap): 1200 GiB peak estimate, 600 GiB request, 720 GiB `memory.max`. See
  `apps/karpenter/scaleodm-nodepool.yaml` in `k8s-infra`.
- Depth-map estimation runs behind two worker threads, so cores past ~48 only
  speed up matching and undistort.
- When the pod has a CPU limit, ODM is given a matching `--max-concurrency`,
  since its own default is every core on the node and would oversubscribe the
  quota. With no limit set (as in prod) ODM keeps that default, and an explicit
  caller value always wins.
- The swap approach works on non-Karpenter clusters that attach swap from local
  disk; see the generic setup in [`swap.md`](./swap.md).
- With plenty of real RAM, skip swap tuning entirely.
