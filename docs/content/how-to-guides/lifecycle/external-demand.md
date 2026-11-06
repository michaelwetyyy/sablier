---
title: Keep workloads awake from external demand
weight: 35
---

Sablier sessions do not have to come from HTTP traffic. The `sablier demand` command bridges external work queues to the same session lifecycle used by reverse-proxy integrations.

This is useful for workloads where demand exists before the workload itself can run, such as CI runners or a KubeVirt VM that hosts an execution agent.

## How it works

Each demand source is polled independently:

1. When the source reports work, the bridge calls Sablier's `poke` strategy for the configured group or instance names.
2. Repeated polls renew the normal Sablier session while work remains.
3. When the source becomes idle, the bridge does nothing. The last session expires after `idle_after` and Sablier applies the provider's normal stop behavior.
4. Source failures default to **fail awake**: the bridge renews the session rather than risking a sleeping worker while queue state is unknown.

The bridge never implements its own scale-to-zero or VM timer. Sablier remains the single owner of the workload lifecycle.

## Run the demand bridge

Create a separate demand configuration file, for example `/etc/sablier/demand.yaml`:

```yaml
poll_interval: 10s
request_timeout: 5s

# Optional Prometheus endpoint for this demand bridge.
metrics:
  listen: :10001

sablier:
  url: http://sablier:10000

sources:
  - name: parhelion
    type: parhelion
    idle_after: 30m
    target:
      group: parhelion
    parhelion:
      url: http://parhelion-controller.parhelion:8000
      token_file: /var/run/secrets/parhelion/control-token
      runner_id: windows-01
      os: windows

  - name: homelab-gitops
    type: gitea-actions
    idle_after: 10m
    target:
      group: gitops-validation
    gitea_actions:
      url: http://gitea.gitea:3000
      token_file: /var/run/secrets/gitea/token
      owner: michael
      repo: homelab-gitops
```

Then run:

```shell
sablier demand --file /etc/sablier/demand.yaml
```

A single demand bridge can manage multiple sources and targets.

## Demand bridge metrics

Set `metrics.listen` to expose a Prometheus endpoint from the demand process itself. The endpoint is separate from the main Sablier server's `/metrics` route so a bridge can be deployed and scraped independently. Keep it on a trusted/internal interface unless you deliberately expose it.

The bridge exports configuration as gauges immediately at startup, before any source has produced work:

- `sablier_demand_target_info` identifies each configured target, source type and failure policy;
- `sablier_demand_target_idle_after_seconds` reports the configured idle window;
- `sablier_demand_source_active` reports the effective demand state after applying the failure policy;
- `sablier_demand_source_check_success` distinguishes a successful source check from fail-awake behavior; and
- `sablier_demand_source_last_check_timestamp_seconds` records when the source was last checked.

No source credentials, tokens or upstream URLs are exposed as metric labels. Because target configuration is exported directly from the loaded demand file, the target inventory remains available even if the main Sablier server restarts and has not yet handled a request for that target.

## Session timing

`poll_interval` controls how quickly new work is noticed. `idle_after` is deliberately implemented as the Sablier session duration, so it is the amount of idle time before the target sleeps.

The bridge requires `idle_after` to be at least twice `poll_interval`. This prevents ordinary polling jitter from accidentally allowing an active session to expire.

For example:

- `poll_interval: 10s` and `idle_after: 10m` gives CI runners a ten-minute anti-thrash window.
- `poll_interval: 10s` and `idle_after: 30m` keeps an interactive VM warm for thirty minutes after its last job.

## Failure policy

The default is:

```yaml
failure_policy: awake
```

If the external queue cannot be inspected, Sablier's session is renewed. This is conservative: temporary API, DNS, or credential problems cannot strand queued work behind a sleeping target.

For non-critical workloads, use:

```yaml
failure_policy: ignore
```

With `ignore`, a failed source check does not renew the session and the target may expire normally.

For queues where an API outage should preserve the last successfully observed demand state, use:

```yaml
failure_policy: last-known
```

`last-known` renews the session after a failed check only when the source was active on its most recent successful check. If the source was last idle, the existing idle timer is left untouched. Before the first successful check, `last-known` behaves like `awake` so startup during an upstream outage cannot strand existing work. This is useful for CI runners: transient API failures do not reset an idle countdown, but a runner with a previously observed in-progress job stays awake.

## Gitea Actions source

By default, `gitea-actions` makes one request for the 20 most recent workflow runs, including pull requests (`exclude_pull_requests=false`). Queued, pending, in-progress, running and waiting runs retain demand. Omitting `runner_labels` or setting it to an empty list preserves this behavior.

For a target that can execute only some jobs, declare its complete runner capabilities:

```yaml
gitea_actions:
  url: https://gitea.example.com
  owner: example
  repo: project
  runner_labels: [validation-check, linux]
```

This optional mode requires a Gitea API supporting repository Actions job listing and job `labels` (verified against Gitea 1.27.3). It requests `/api/v1/repos/{owner}/{repo}/actions/jobs` with repeated `status=queued`, `status=in_progress`, and `status=pending` filters, sorted by ascending ID. These filters select waiting, running, and blocked jobs. It scans active jobs directly, so terminal workflow history cannot hide demand behind the default 20-run window. Pull-request jobs are included.

Every required label of an active job must appear in `runner_labels`; sharing one label is insufficient. These are declared capabilities, not labels discovered from an online runner, so a sleeping target can still be selected. Blank and duplicate configured labels are rejected. For example, a helper declaring `validation-check` excludes jobs requiring only `validation-primary`, and excludes jobs requiring both labels. Keep primary-only smoke tests or privileged jobs on their intended target; declaring extra capabilities does not change scheduling or permissions.

Pages contain up to 50 jobs. A poll must account for the complete `total_count`, with at most 100 pages (5,000 jobs). Missing or malformed active-job labels, unsupported responses, API failures, changing totals, incomplete or repeated pages, and the page limit produce a source error rather than an idle result. Requests use fixed endpoints and do not follow redirects. A positive result requires a complete valid scan too. Configure the existing failure policy for uncertainty: `awake` retains demand, `ignore` permits expiry, and `last-known` preserves the last successful observation (failing awake before the first success).

The token file is optional for publicly readable repositories and is read again on every poll so Kubernetes Secret rotation does not require restarting the bridge.

## Parhelion source

The `parhelion` source reads the normal `/api/v1/runs` control endpoint. The bridge considers `queued`, `assigned`, `running`, and `needs_attention` runs active. Optional `runner_id` and `os` values narrow demand to the runner being controlled.

This source uses Parhelion's existing public control contract; it does not depend on a special autosuspend endpoint or Parhelion-owned idle timer.
