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
    "options": "[{\"name\":\"sfm-algorithm\",\"value\":\"triangulation\"},{\"name\":\"rolling-shutter\",\"value\":true}]"
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
| `boundary=<AOI GeoJSON>` | Clips output to one Polygon AOI and can greatly reduce render cost. Pass inline GeoJSON or an `s3://` URL; see [Boundary](./nodeodm-compatibility.md#boundary). |
| `rolling-shutter` | Corrects the skew from the sensor reading out row by row while the aircraft moves. An electronic shutter at 12 m/s and a 4 cm GSD is ~6 px of skew. ODM holds a readout time per camera model (`opendm/rollingshutter.py`), but an unlisted camera silently gets a 30 ms guess, so check the log line and set `rolling-shutter-readout` if the camera is missing. |
| `split=400` + `split-overlap=150` | Caps submodel size on stock ODM, where reconstruction cost grows superlinearly. |

`sfm-algorithm=triangulation` on an image without OpenSfM 1.0 fails at the
sparse stage, so change one variable at a time and keep a stock-image run of the
same dataset to compare against.

### Flags to avoid on large datasets

These buy speed by removing global consistency. Stacked on a 12k-image scene they
produced a reconstruction spanning 20 x 13 km over a 3 x 3 km site; dropping them
brought it back to 3 x 3 km.

| Flag | Why not |
| --- | --- |
| `matcher-neighbors=<n>` | Any value > 0 forces `matching_graph_rounds: 0` in ODM/ODX, removing the long-range candidate pairs that make a large block globally rigid. Leave it unset to get 20 rounds (ODX) / 50 (ODM). |
| `use-hybrid-bundle-adjustment` | Sets `local_bundle_radius: 1` (immediate neighbours only). Unset gives `0`, which uses the global solver. |
| `auto-boundary` | Buffers the convex hull of the camera shots by `avg_gsd * 100`, where the GSD is in cm but the buffer is applied in metres - a 4.3 cm GSD becomes a 433 m buffer. It rarely trims anything useful. |

Independently of any flag, ODX/OpenSfM swaps global bundle adjustment for a
stochastic 500-camera-per-round solver above **4,000 shots**, and that path holds
camera intrinsics fixed - `optimize_camera_parameters` goes silently inert and
`cameras.json` comes back with every distortion coefficient at `0.0`. `--split`
under 4,000 images is the only way to get focal length and distortion refined.

## Defects no flag can fix

Measured on a 12,000-image project flown with four DJI Mini 4 Pro airframes.
All three need fixing at ingest, before ODM sees the imagery.

- **The altitude datum resets on every power cycle.** `AbsoluteAltitude` is
  `RelativeAltitude` plus a constant fixed at power-on: across 36 flights the
  difference had a standard deviation of 0.0000 in 33 of them. Two flights over
  the same ground 37 minutes apart reported altitudes 92 m apart, median 41 m
  across all co-located pairs. ODM picks matching candidates in 3D, so repeat
  passes over the same ground drop out of each other's candidate list and never
  match. Use `RelativeAltitude`, or normalise onto one datum against a DEM and
  write it back into the EXIF, since ODM reads the file and not your database.
- **Digital zoom does not change the reported focal length.**
  `DigitalZoomRatio` was 2 on 11.9% of images, which on a Mini 4 Pro is a
  sensor crop, yet `FocalLength`, `FocalLengthIn35mmFormat` and `FOV` were
  identical on every image. ODM fits one camera model across images whose real
  focal length differs by 2x. Drop the zoomed frames, or rewrite their EXIF focal
  so ODM builds a second camera model - dropping 12% of a set punches holes if the
  zoom is interleaved rather than whole flights.
- **Nadir is not -90.** Gimbal pitch was exactly -80.0 on 12,145 of 12,338
  images, consistent across all four aircraft, so it is the platform and not a
  crew choice. A filter expressed as a tolerance around -90 will not do what it
  says. Derive the held angle from the data.

A 249 g airframe with an electronic shutter, no RTK and `SurveyingMode: 0` will
not match survey-grade kit whatever flags it is processed with.

## Notes

Tasks submitted this way are invisible to Drone TM: its reconcile looks up
`odm_task_uuid` in its own database, so read results from the workflow logs and
the `writeS3Path` prefix. `POST /task/restart` does not carry `odmImage` over,
so restart by submitting a new task.
