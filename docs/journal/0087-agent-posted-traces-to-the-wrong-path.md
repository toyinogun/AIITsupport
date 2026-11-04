# 0087. The agent posted its traces to "/" and the Collector answered 404

**Date:** 2026-11-04 · **Week:** 3 · **Feature:** #18 Observability & ops dashboard
**Time lost:** ~30 min (estimate) · **Story worthy:** both

## What I saw
On k3sprox, after Grafana came up, the agent logged `telemetry_export_failed` with `cause: other` once a minute, starting 5 seconds after each start. The Collector log showed no error, a POST of `{}` to `/v1/traces` from a pod with the agent's labels returned 200, and Cilium showed no policy drops.

## What I thought it was
A network policy or Cilium blocking the agent's egress to the Collector. (The cause token was not `network`, and the test pod got through.)

## What it actually was
`Setup` passed the endpoint to `otlptracehttp.WithEndpointURL`, which uses the URL path as given. The chart, Compose, and the runbook all set the endpoint as a base URL (`http://otel-collector:4318`), so spans went to `/`, and the Collector answered `404 page not found`. The old test used an endpoint that already ended in `/v1/traces`, and no test checked the path the server saw, so nothing failed until a real cluster. Reproduced from the laptop through a port-forward: `failed to send to http://127.0.0.1:14318/: 404 Not Found`.

## How I fixed it
`tracesURL` adds `/v1/traces` when the endpoint has no path (the OpenTelemetry standard for a base URL) and keeps an explicit path. Tests written first: `TestSetupBaseEndpointExportsToTracesPath`, `TestSetupKeepsAnExplicitPath`. Branch `fix/otlp-traces-path`.

## What I would tell a team doing this for real
(draft) The log line hid the error text on purpose, so a safe log cost me the diagnosis: keep a one-line way to see the real exporter error in dev, and test the path a server sees, not only that Setup returns without error.

## Evidence
- Error text: `traces export: failed to send to http://127.0.0.1:14318/: 404 Not Found (body: 404 page not found)`
- Commit or PR: see the PR for branch `fix/otlp-traces-path`
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
