# ScaleODM

<!-- markdownlint-disable -->
<p align="center">
  <em>Kubernetes-native auto-scaling and load balancing for OpenDroneMap.</em>
</p>
<p align="center">
  <a href="https://github.com/hotosm/ScaleODM/actions/workflows/release.yml" target="_blank">
      <img src="https://github.com/hotosm/ScaleODM/actions/workflows/release.yml/badge.svg" alt="Build & Release">
  </a>
  <a href="https://github.com/hotosm/ScaleODM/actions/workflows/test.yml" target="_blank">
      <img src="https://github.com/hotosm/ScaleODM/actions/workflows/test.yml/badge.svg" alt="Test">
  </a>
</p>

---

<!-- markdownlint-enable -->

## Usage

1. Run in a Kubernetes cluster, via provided Helm chart.
2. Use the ScaleODM API as you would NodeODM.
3. ScaleODM will handle task distribution to NodeODM
   instances in the cluster, or defer to other ScaleODM
   instances in other clusters.
4. There is a separate SplitMerge option for large datasets,
   which uses Argo workflows underneath.

## Development

- Binary and container image distribution is automated on new **release**.

### Run The Tests

The test suite depends on a database, so the most convenient way is to run
via docker.

There is a pre-configured `compose.yml` for testing:

```bash
docker compose run --rm scaleodm
```
