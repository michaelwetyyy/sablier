---
title: Kubernetes
weight: 3
---

This tutorial connects Sablier to Kubernetes. You will select the Kubernetes provider, grant Sablier the roles it needs to use the Kubernetes API, register a Deployment so Sablier can scale it, and confirm Sablier knows when the Deployment is ready. Sablier assumes that it is deployed within the Kubernetes cluster to use the Kubernetes API internally.

## Select the Kubernetes provider

Set the [provider.name](/reference/cli/) property to `kubernetes`.

{{< tabs >}}
{{< tab name="File (YAML)" >}}

```yaml
provider:
  name: kubernetes
```

{{< /tab >}}
{{< tab name="CLI" >}}

```bash
sablier start --provider.name=kubernetes
```

{{< /tab >}}
{{< tab name="Environment Variable" >}}

```bash
SABLIER_PROVIDER_NAME=kubernetes
```

{{< /tab >}}
{{< /tabs >}}

{{< callout type="warning" >}}
**Ensure that Sablier has the necessary roles.**
{{< /callout >}}

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: sablier
rules:
  - apiGroups:
      - apps
      - ""
    resources:
      - deployments
      - statefulsets
    verbs:
      - get     # Retrieve info about specific dep
      - list    # Events
      - watch   # Events
  - apiGroups:
      - apps
      - ""
    resources:
      - deployments/scale
      - statefulsets/scale
    verbs:
      - patch   # Scale up and down
      - update  # Scale up and down
      - get     # Retrieve info about specific dep
      - list    # Events
      - watch   # Events
  # Only required if you manage KubeVirt VirtualMachines (see below).
  - apiGroups:
      - kubevirt.io
    resources:
      - virtualmachines
    verbs:
      - get     # Retrieve VM state
      - list    # Discovery and events
      - watch   # Events
      - patch   # Switch spec.runStrategy between Always and Halted
  # Only required if you manage CloudNativePG Clusters (see below).
  - apiGroups:
      - postgresql.cnpg.io
    resources:
      - clusters
    verbs:
      - get     # Retrieve info about a specific cluster
      - list    # Discovery and events
      - watch   # Events
      - patch   # Toggle the hibernation annotation
  # Only required if you manage OT-CONTAINER-KIT Redis instances (see below).
  - apiGroups:
      - redis.redis.opstreelabs.in
    resources:
      - redis
    verbs:
      - get     # Resolve the Redis CR owner of a StatefulSet
      - patch   # Toggle the skip-reconcile annotation
```

{{< callout type="info" >}}
The `kubevirt.io`, `postgresql.cnpg.io` and `redis.redis.opstreelabs.in` rules are optional. Sablier skips those integrations gracefully when their CRDs are absent.
{{< /callout >}}

## Register a Deployment

For Sablier to work, it needs to know which deployments to scale up and down. Register a Deployment by opting in with labels:


```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: whoami
  labels:
    app: whoami
    sablierapp.dev/enable: "true"
    sablierapp.dev/group: mygroup
spec:
  selector:
    matchLabels:
      app: whoami
  template:
    metadata:
      labels:
        app: whoami
    spec:
      containers:
      - name: whoami
        image: acouvreur/whoami:v1.10.2
```

## Confirm when the deployment is ready

Sablier checks for the deployment replicas. As soon as the current replicas matches the wanted replicas, then the deployment is considered `ready`.

{{< callout type="info" >}}
Kubernetes uses the Pod healthcheck to check if the Pod is up and running. So the provider has a native healthcheck support.
{{< /callout >}}

## Configure with labels or annotations

On Kubernetes, Sablier keys use the public `sablierapp.dev/` prefix. To get the Kubernetes key of a key in the [Label reference](/reference/labels/), replace `sablier.` with `sablierapp.dev/`. For example, `sablier.idle.replicas` becomes `sablierapp.dev/idle.replicas`.

You can set each key as a **label** or as an **annotation**, with one exception: `sablierapp.dev/enable` must always be a label (see the note below). This applies to Deployments, StatefulSets, KubeVirt VirtualMachines, CloudNativePG Clusters and OT-CONTAINER-KIT Redis instances.

Annotations are useful because Kubernetes **label values are restricted** (max 63 characters, only `[A-Za-z0-9._-]`, no commas or colons). Some Sablier values cannot be expressed as labels and must use annotations, for example:

- `sablierapp.dev/group` with multiple comma-separated groups (e.g. `team-a,team-b`)
- `sablierapp.dev/running-hours` (e.g. `09:00-18:00`), where the colon is invalid in a label value
- `sablierapp.dev/running-days` (e.g. `Mon,Tue,Wed,Thu,Fri`)

When the same key is present as both a label and an annotation, the **annotation takes precedence**.

{{< callout type="warning" >}}
`sablierapp.dev/enable` must be set as a **label**. Workload discovery relies on a server-side label selector, which cannot match annotations. All other keys work as labels or annotations.
{{< /callout >}}

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: whoami
  labels:
    app: whoami
    sablierapp.dev/enable: "true"       # must be a label
  annotations:
    sablierapp.dev/group: "team-a,team-b"          # comma is invalid as a label value
    sablierapp.dev/running-hours: "09:00-18:00"    # colon is invalid as a label value
    sablierapp.dev/running-days: "Mon,Tue,Wed,Thu,Fri"
spec:
  # ...existing spec...
```

### Legacy `sablier.*` keys

Sablier continues to read the `sablier.*` keys, for example `sablier.enable` and `sablier.group`. On Kubernetes, these keys are deprecated. A key without a prefix is [private to the user](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#syntax-and-character-set), thus it can conflict with the label conventions of your organization. Use the `sablierapp.dev/` keys for new workloads.

During a migration, a workload can have the two forms of a key. In this case, the `sablierapp.dev/` key takes precedence over the `sablier.` key from the same source. An annotation continues to take precedence over a label.

## Register KubeVirt VirtualMachines

Sablier can manage [KubeVirt](https://kubevirt.io/) `VirtualMachine` resources alongside Deployments and StatefulSets. VMs use KubeVirt's declarative `spec.runStrategy` instead of a replica count:

- **Stop** sets `spec.runStrategy: Halted`.
- **Start** sets `spec.runStrategy: Always`.

When Sablier takes ownership of an older VM that still uses the deprecated `spec.running` boolean, the first start/stop transition removes `spec.running` and writes `spec.runStrategy` so the two mutually exclusive fields are never left set together.

Opt in on the `VirtualMachine` itself:

```yaml
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: windows-01
  namespace: parhelion
  labels:
    sablierapp.dev/enable: "true"
    sablierapp.dev/group: parhelion
spec:
  runStrategy: Halted
  template:
    # ... existing KubeVirt VM template ...
```

The names-based API identifier is `virtualmachine_<namespace>_<name>_1` with the default `_` delimiter, for example `virtualmachine_parhelion_windows-01_1`.

### VirtualMachine readiness

A KubeVirt VirtualMachine is considered:

- `stopped` when its desired run strategy is `Halted` (or legacy `spec.running` is `false`);
- `ready` when KubeVirt reports `status.ready: true` or `status.printableStatus: Running`;
- `error` for KubeVirt terminal/error printable states such as `Unschedulable`, `PvcNotFound`, `ErrImagePull` or `DataVolumeError`;
- `starting` otherwise.

Sablier also watches changes to the VM's desired running state, so a VM started or halted outside Sablier participates in the normal lifecycle event stream.

## Register CloudNativePG Clusters

Sablier can also start and stop [CloudNativePG](https://cloudnative-pg.io/) `Cluster` resources. Instead of scaling a replica count, Sablier toggles CloudNativePG's [declarative hibernation](https://cloudnative-pg.io/documentation/current/declarative_hibernation/) annotation:

- **Stop** sets `cnpg.io/hibernation: "on"`, and the operator scales the cluster down and removes its workload while keeping the PVCs.
- **Start** sets `cnpg.io/hibernation: "off"`, and the operator resumes the cluster.

Opt-in with the same labels used for deployments and statefulsets:

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: opencell-db
  labels:
    sablierapp.dev/enable: "true"
    sablierapp.dev/group: opencell
spec:
  instances: 3
  storage:
    size: 1Gi
```

This makes it possible to put an application, its Keycloak and its database in a single `sablierapp.dev/group`, so that a single request wakes up the whole stack and inactivity hibernates all of it.

### Cluster readiness

A CloudNativePG Cluster is considered:

- `stopped` when the `cnpg.io/hibernation` annotation is `"on"`;
- `ready` when `status.readyInstances` is greater than or equal to `spec.instances`;
- `starting` otherwise.

{{< callout type="info" >}}
Resuming a hibernated cluster takes longer than a simple scale-up (PVC reattachment and PostgreSQL recovery). Make sure your reverse-proxy timeouts allow for it.
{{< /callout >}}

## Register OT-CONTAINER-KIT Redis instances

Sablier can scale to zero StatefulSets managed by the [OT-CONTAINER-KIT redis-operator](https://github.com/OT-CONTAINER-KIT/redis-operator). The operator continuously reconciles its StatefulSets back to the desired replica count, so a plain scale-to-zero is immediately undone. Sablier works around this by toggling the operator's own pause mechanism:

- **Stop** sets `redis.opstreelabs.in/skip-reconcile: "true"` on the Redis CR, then scales the StatefulSet to 0.
- **Start** scales the StatefulSet back to 1, then removes the annotation so the operator resumes normal reconciliation.

If the scale fails, the annotation is cleared immediately so the operator is never left paused with pods still running.

Opt-in by adding the standard Sablier labels to the Redis CR. The operator propagates them to the StatefulSet it manages, so no extra labelling is needed:

```yaml
apiVersion: redis.redis.opstreelabs.in/v1beta2
kind: Redis
metadata:
  name: myapp-redis
  labels:
    sablierapp.dev/enable: "true"
    sablierapp.dev/group: myapp
spec:
  kubernetesConfig:
    image: quay.io/opstree/redis:v7.0.12
```

This makes it straightforward to group a Redis instance with the rest of an application stack so that a single request wakes everything up and inactivity shuts it all down together.

{{< callout type="info" >}}
The `redis.redis.opstreelabs.in` RBAC rule (see above) is required for Sablier to patch the skip-reconcile annotation on the Redis CR. If the rule is absent or the CRD is not installed, Sablier logs a warning and scales the StatefulSet anyway. The operator will reconcile replicas back, but no other harm is done.
{{< /callout >}}
