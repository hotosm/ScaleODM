# Memory sizing

How much RAM (and swap) an ODM run needs, by image count. For how ScaleODM turns
these into pod requests and limits see [`swap.md`](./swap.md) and
[`decisions/0003-swap.md`](../decisions/0003-swap.md).

## The short version

- RAM demand is a brief **peak**, not sustained.
- The table is RAM **+ swap**. Provision less real RAM and let swap cover the peak.
  With ~2x swap a 62Gi machine handles ~5000 images.
- Past ~5000 images the peak lands in OpenMVS `DensifyPointCloud` and grows faster
  than linearly. It scales with **view count**, not depth-map resolution.
- Always pass a real `--boundary`. Render cost tracks reconstruction *extent*, so
  bad geometry is a sizing problem too.

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

Don't go much past 12000 in one scene. Above that use `--split` /
`--split-overlap` and let ODM build and merge submodels. Still one pod, no fan out.

## Prod sizing at 12k

`memoryMaxGiB 1200`, `swapRatio 1.0`, `memoryLimitMarginPercent 20`,
`cpuPerGiB 0.125`, `ephemeralGiBPerGiBRAM 0.3`:

| | peak est | request | `memory.max` | CPU | ephemeral | node |
|---|---|---|---|---|---|---|
| 12k | 800 | 400 GiB | 480 GiB | 50 | 240 GiB | `r6id.16xlarge` |

- Sizing counts **S3 objects**, ODM counts **usable images**. One 12k run sized off
  >= 12000 while ODM loaded 11782.

## Observed

64 GB RAM (12 core), no swap: ~4000 image ceiling. With ~2x swap (127 GB):

- Comfortable to ~1600, swap nearly gone at 1869.
- 2658 images: out of swap, OOM-killed in OpenMVS, recovers and finishes.
- 4778 images: OOM-killed in OpenMVS, again in mvs-texturing, dies.
- ~144 GB peak at 3000 images, so ~4000 per submodel is the practical limit.
  Freetown WJ (34k images) used `--split 8000`.

~12k images, ODX 3.8.3, `--sfm-algorithm triangulation`. **Same imagery both
columns**, the difference is `--boundary` plus dropping `--matcher-neighbors` and
`--use-hybrid-bundle-adjustment`:

| | Unbounded, 384 GiB node | `--boundary`, 768 GiB node |
|---|---|---|
| `memory.max` | 360 GiB | 720 GiB |
| Working-set peak | 932 GiB (`medium`, killed) / 709 GiB (`low`) | 649 GiB |
| `DensifyPointCloud` `VmPeak` | **709 GiB** | **89.8 GiB** |
| Swap used | 344 GiB, 27 min pinned | **0 B** |
| `odm_dem` + `odm_orthophoto` | pinned `memory.max` **1h52m**, 419 GiB swap | no pressure |
| Ortho / DEM raster | 77 / 46.8 Gpixels, 92% cropped | 8.24 / 4.18 Gpixels, 0% cropped |
| Wall clock | 17h57m | 11h47m |

- Quartering depth-map pixels moved the peak only ~25%, so `pc-quality` is the
  wrong flag to optimise. Size the node instead.
- Fusion 67 GiB, filtering 18 GiB, meshing 45 GiB.
- **Treat the working-set peak as an upper bound, not demand.** The biggest single
  process in the bounded run was 89.8 GiB; the 649 GiB is `texrecon` on 96 threads
  streaming ~300 GiB of TIFs, and working set counts active page cache. Check the
  anon vs cache panel before sizing off it.

## Where the wall clock goes

**46% of the run sits under 3 of 96 cores.** Inside the `opensfm` stage:

| Window | Duration | Median cores | What |
|---|---|---:|---|
| 02:32 - 04:06 | 94 min | **91.4** | features + matching pass 1, genuinely parallel |
| 04:08 - 07:48 | 3h40m | ~10 | reconstruct, rolling-shutter correct, re-match, reconstruct |
| 07:48 - 09:56 | 130 min | **2.1** | undistort |

Matching saturates the node. Reconstruction and bundle adjustment are inherently
serial. **No resource change fixes that.**

## Workspace disk

`--dsm`/`--dtm` profile is `multiplier 12`, `gibPerImage 0.15`, `maxGiB 2048`.
`estimateWorkspaceGiB` takes `max(bytes x multiplier, count x gibPerImage)` then
clamps, so at ~12k the count floor gives ~1800 GiB against ~1400 GiB from bytes.
The floor sets the size, the clamp doesn't bind. Confirmed: a 12k run got a
**1902 GiB** PVC.

| Observation | % of PVC | Absolute | Per image |
|---|---:|---:|---:|
| `opensfm` plateau (JPEGs + features) | 8.2% | 151 GiB | |
| Peak, `pc-quality low` + boundary | 29.4% | 559 GiB | 0.047 GiB |
| Peak, `pc-quality medium`, unbounded | 63.6% | ~1200 GiB | 0.100 GiB |

`gibPerImage 0.15` is ~1.5x a measured `pc-quality medium` run. **Size is fine.**

**Throughput is fine too.** Undistort wrote **303 GiB over ~104 min = 40 MiB/s
sustained**, against the gp3 baseline of 125 MiB/s. Nowhere near capped.

> [!NOTE]
> `kubelet_volume_stats_used_bytes` as a percentage resolves to 0.1%, which is
> 1.9 GiB on a 1902 GiB volume. Every delta is a multiple of that, so apparent
> write bursts are quantisation. Use the node-exporter throughput panel for rates.

**IOPS might not be**, and CPU can't tell you: container CPU excludes iowait, so a
job blocked on disk and a job running two threads look identical. 40 MiB/s at the
3000 IOPS baseline is ~14 KiB/op. Check `node_disk_io_time_seconds_total`
(utilisation, 1.0 = saturated) and `node_disk_{reads,writes}_completed_total`
(flat at 3000 = capped). Only raise the class if one of those pins.

If it does: `workspaceStorageClass.parameters` is passed straight through and Helm
deep-merges, so `throughput: "500"` / `iops: "6000"` is a values-only change. But
StorageClass `parameters` are immutable, so the class has to be deleted and
recreated. Safe between jobs, since it's only read at PVC provisioning time.

## Notes

- **Swap only engages when the cgroup hits `memory.max`.** At 720 GiB against a
  649 GiB peak it never does and node RAM absorbs it. To make swap absorb a
  transient, `memory.max` has to sit below the peak.
- **CPU over-provisioning is free.** 76 cores requested against a median of 6.12,
  but 600 GiB of RAM already selects a 96-vCPU instance so CPU never picks the
  node, and matching does use all 96. Lowering the request buys nothing.
- Depth-map estimation runs behind two worker threads, so cores past ~48 only help
  matching and undistort.
- With a CPU limit set, ODM gets a matching `--max-concurrency`, since its default
  is every node core and would oversubscribe the quota. With no limit (prod) it
  keeps that default, and an explicit caller value always wins.
- Workspace PVCs are collected when workflows finish (`volumeClaimGC`).
- With plenty of real RAM, skip swap tuning entirely.
