# Memory sizing

How much RAM (and swap) an ODM run needs, scaled by image count. For how
ScaleODM turns these numbers into pod requests/limits, see
[`swap.md`](./swap.md) and [`decisions/0003-swap.md`](../decisions/0003-swap.md).

## The short version

RAM demand is a brief **peak** during `mvs-texturing`, not a sustained resident
set. With swap at ~2x RAM to absorb that spike, a 62Gi machine can process ~5000
images. The linear estimate below is total RAM **+ swap**; provision real RAM
lower and let swap cover the peaks.

## Linear scaling estimate

| Images | RAM + swap |
|-------:|-----------:|
| 40     | 4 GB       |
| 250    | 16 GB      |
| 500    | 32 GB      |
| 1500   | 64 GB      |
| 2500   | 128 GB     |
| 3500   | 192 GB     |
| 5000   | 256 GB     |

Extrapolating: ~13k images needs ~200 GB resident / 600-700 GB with swap, and
~45k images is roughly the ceiling for 768 GB RAM + ~1.5 TB swap (assuming a
Ceres integer overflow doesn't stop you first).

## Observed datapoints

64 GB RAM machine (12 core):

- **Without swap:** ~4000 image ceiling.
- **With ~2x swap (127 GB):**
  - 1589 images: barely touches swap.
  - 1869 images: nearly exhausts swap.
  - 2658 images: runs out of swap, OOM-killed in OpenMVS, but recovers and finishes.
  - 4778 images: OOM-killed in OpenMVS (survives), then OOM-killed again in mvs-texturing and dies.

Peak usage was ~144 GB for 3000 images on a 64 GB + swap server, which lines up
with a practical ceiling near 4000 images per submodel. Freetown WJ (34k images)
ran with `--split 8000` to keep each submodel within these bounds.

## Notes

- On AWS/Karpenter, `r7iz.8xlarge` benchmarked faster than `r8i.8xlarge`, but
  Karpenter picks whatever capacity is available per run, so this rarely matters.
- The same swap approach should work on non-Karpenter clusters that attach swap
  from local disk instead of via Karpenter node config; see the generic setup in
  [`swap.md`](./swap.md).
- With a machine that has plenty of RAM, you can skip swap tuning entirely.
