# Plain Logs Instead of Built-in OpenTelemetry

## Context and Problem Statement

ScaleODM used to ship an optional OpenTelemetry module (`app/observability`).
It pushed OTLP traces and metrics for the API: request counts and latency,
submission failures, reconciliation and readiness counters.

On review, we determined it didn't add much benefit:

- The Argo controller, cAdvisor and kube-state-metrics already tell us job
  counts, phases, durations, failures, OOM kills and resource use. That's what
  our Grafana dashboard is built on.
- The traces only covered the API, not the ODM processing itself (the part
  that actually goes wrong), and were sampled at 10%.
- Metrics are just counts. When a job fails, what we want to know is which
  request it was and what was in it, so we can replay it. The logs already
  have that.
- To use it at all, we'd have needed an OTLP collector in the cluster, as the
  gRPC exporters don't fit the usual Prometheus scrape setup.

## Considered Options

- **Keep the OTel module.** It's vendor-neutral, but it's ~450 lines plus a
  pile of dependencies to maintain, for signals we mostly already have.
- **Swap it for a Prometheus `/metrics` endpoint.** This fits how everything
  else in the cluster is scraped, but we'd still be keeping metrics nobody
  looks at.
- **Remove it and rely on logs.** ScaleODM only runs on Kubernetes, so anyone
  deploying it already has (or should have) something collecting container
  logs, like Loki.

## Decision Outcome

We removed the module.

ScaleODM just logs to stdout, including the request parameters and a stable
`reason=<code>` when a `/task/new` call is rejected. Collecting, searching and
alerting on those logs is left to whatever log stack the cluster runs.

## Consequences

- No app-level metrics or traces. Cluster metrics (Argo, cAdvisor,
  kube-state-metrics) are still where dashboards come from.
- Failures are found and replayed from the logs, e.g. in Loki:
  `{namespace="odm"} |= "rejected reason="`.
- Alerting on failure rates comes from log rules (e.g. the Loki ruler),
  rather than app counters.
- If we ever do need per-request metrics, the simplest option is a Prometheus
  `/metrics` endpoint with a `ServiceMonitor`, not an OTLP push pipeline.
