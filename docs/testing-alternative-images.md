# Testing alternative ODM images

Workflows normally run `config.odmImage`. To trial a fork or a new release
without redeploying, allowlist the image and pass `odmImage` per task.

```yaml
config:
  odmImage: "ghcr.io/hotosm/odm:3.6.1"
  allowedOdmImages: "ghcr.io/hotosm/odm,docker.io/opendronemap/odm,docker.io/webodm/odx"
```

Entries match a repository, not a tag, so any tag or digest of a listed repo is
accepted and a new release needs no config change. Bare names are read as
Docker Hub, so `webodm/odx` and `docker.io/webodm/odx` are the same entry.
Matching is exact per repo: `webodm/odx-evil` and `evil.io/webodm/odx` are both
rejected. An `odmImage` outside the allowlist is rejected with a 400, and an
empty `allowedOdmImages` rejects every override.

## Submitting a task

```bash
curl -X POST http://scaleodm:31100/task/new \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "odx-trial",
    "readS3Path": "s3://dronetm-prod/trials/dataset-a/images/",
    "writeS3Path": "s3://dronetm-prod/trials/dataset-a/odx/",
    "odmImage": "webodm/odx",
    "capacityType": "on-demand",
    "options": "[{\"name\":\"sfm-algorithm\",\"value\":\"triangulation\"},{\"name\":\"matcher-neighbors\",\"value\":24}]"
  }'
```

`options` is a JSON array encoded as a string, matching NodeODM. Each entry
becomes `--name=value`, so only real ODM flags belong there; `odmImage` and
`capacityType` are top level because they configure the pod, not ODM.

Use `on-demand` so a spot eviction cannot be mistaken for a crash. The workflow
still stops at `activeDeadlineSeconds` (48h default), so pick a dataset that
reaches the dense stage well inside that.

Poll it, then read the logs:

```bash
curl http://scaleodm:31100/task/<uuid>/info
kubectl logs -l workflows.argoproj.io/workflow=<uuid> --tail=200
```

## Flags worth setting

| Flag | Why |
| --- | --- |
| `sfm-algorithm=triangulation` | Places cameras from GPS instead of adding them one at a time. Needs OpenSfM 1.0, so ODX only. |
| `boundary=<AOI GeoJSON>` | Clips the reconstruction to the real project area. Prefer this over `auto-boundary`. |
| `split=400` + `split-overlap=150` | Caps submodel size on stock ODM, where reconstruction cost grows superlinearly. |

`sfm-algorithm=triangulation` on an image without OpenSfM 1.0 fails at the
sparse stage, so change one variable at a time and keep a stock-image run of the
same dataset to compare against.

### Flags to avoid on large datasets

Both of these were previously recommended here. They buy speed by removing
global consistency, and stacked on a 12k-image scene they produced a
reconstruction spanning 20 x 13 km over a 3 x 3 km site.

| Flag | Why not |
| --- | --- |
| `matcher-neighbors=<n>` | Any value > 0 forces `matching_graph_rounds: 0` in ODM/ODX, removing the long-range candidate pairs that make a large block globally rigid. Leave it unset to get 20 rounds (ODX) / 50 (ODM). |
| `use-hybrid-bundle-adjustment` | Sets `local_bundle_radius: 1` (immediate neighbours only). Unset gives `0`, which uses the global solver. |
| `auto-boundary` | Buffers the convex hull of the camera shots by `avg_gsd * 100`, where the GSD is in cm but the buffer is applied in metres — a 4.3 cm GSD becomes a 433 m buffer. It rarely trims anything useful. |

Independently of any flag, ODX/OpenSfM swaps global bundle adjustment for a
stochastic 500-camera-per-round solver above **4,000 shots**, and that path holds
camera intrinsics fixed. Keeping submodels under 4,000 images is the only way to
get focal length and distortion refined.

## Notes

Tasks submitted this way are invisible to Drone TM: its reconcile looks up
`odm_task_uuid` in its own database, so read results from the workflow logs and
the `writeS3Path` prefix. `POST /task/restart` does not carry `odmImage` over,
so restart by submitting a new task.
