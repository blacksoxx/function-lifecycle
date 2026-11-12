# function-lifecycle
[![CI](https://github.com/upbound/function-lifecycle/actions/workflows/ci.yml/badge.svg)](https://github.com/upbound/function-lifecycle/actions/workflows/ci.yml)

A [composition function][functions] that sets the lifecycle mode of every
composed managed resource (MR) in one place.

Crossplane v2 namespaced MRs have no `deletionPolicy`. Their
[`managementPolicies`][policies] decide what Crossplane may do: observe, create,
update, delete, or late-initialize. Instead of patching that field into every
resource of every composition, add this function after the steps that
compose resources, and pick a mode.

| Mode | `managementPolicies` | Use it to |
|---|---|---|
| `Full` (default) | `["*"]` | Manage the whole lifecycle. Standard Crossplane behavior. |
| `Provision` | `[Observe, Create, Update, LateInitialize]` | Create, import, and update, but never delete. Deleting the XR orphans its external resources. |
| `ObserveOnly` | `[Observe]` | Import and watch without changing anything. Safe takeover or change freeze. |
| `Deprovision` | `[Observe, Create, Delete]` | Import and delete without reconciling drift. Decommissioning. |

## Usage

Add the function after the steps that compose resources, and before steps
that only read them, such as function-auto-ready:

```yaml
apiVersion: apiextensions.crossplane.io/v1
kind: Composition
metadata:
  name: apps.example.crossplane.io
spec:
  compositeTypeRef:
    apiVersion: example.crossplane.io/v1
    kind: App
  mode: Pipeline
  pipeline:
  - step: compose
    functionRef:
      name: function-patch-and-transform
    input: {} # Your resources
  - step: lifecycle
    functionRef:
      name: function-lifecycle
    input:
      apiVersion: lifecycle.fn.crossplane.io/v1beta1
      kind: Input
      mode: Full                               # Optional. Defaults to Full.
      modeRef: spec.parameters.managementMode  # Optional.
```

The function picks the mode from the first of these that's set:

1. The XR annotation `lifecycle.fn.crossplane.io/mode`. Operators can always
   override the mode with `kubectl annotate`.
2. The field `modeRef` points to. It must start with `spec.`, `status.`, or
   `context.`. Use brackets for keys that contain dots, for example
   `context.[apiextensions.crossplane.io/environment].managementMode` to read
   an EnvironmentConfig loaded by [function-environment-configs][envconfigs].
3. The input's `mode`.
4. `Full`.

An unknown mode is a fatal error. The function never falls back to `Full` when
a mode is misspelled.

The function reports the active mode in an event and in the XR's
`LifecycleMode` condition:

```console
$ crossplane resource trace app my-app
```

### Recipes

Freeze changes to an XR's resources during an incident:

```shell
kubectl annotate app my-app lifecycle.fn.crossplane.io/mode=ObserveOnly
```

Undo it:

```shell
kubectl annotate app my-app lifecycle.fn.crossplane.io/mode-
```

Delete an XR but keep its cloud resources, for example to move them to another
control plane:

```shell
kubectl annotate app my-app lifecycle.fn.crossplane.io/mode=Provision
kubectl get managed -l crossplane.io/composite=my-app
```

Wait until the composed resources show the new policies and are `Synced`,
then delete the XR:

```shell
kubectl delete app my-app
```

> [!IMPORTANT]
> Crossplane applies the new policies on the next reconcile. Wait for it before
> you delete the XR, or Crossplane deletes the external resources.

### What the function changes

* It sets `spec.managementPolicies` on desired resources that have
  `spec.forProvider`, which every MR has. Plain Kubernetes objects are skipped.
* A composed resource that is itself an XR gets the
  `lifecycle.fn.crossplane.io/mode` annotation with the resolved mode instead,
  so its own composition's function-lifecycle step applies the same mode and a
  whole tree follows one switch. The function tells an XR by the
  `spec.crossplane` block Crossplane keeps its own fields in: on the desired
  resource when the composition sets it, otherwise on the observed resource,
  so an XR the composition doesn't pin to a composition is annotated from its
  second reconcile on.
* It overwrites `spec.managementPolicies` set by earlier pipeline steps.
* The provider must support management policies. They're on by default in the
  upjet providers and provider-kubernetes, and off by default in the classic
  crossplane-contrib/provider-aws.

## Set the mode from an Operation

The function also runs as a step of an [Operation][operations]: a pipeline that
runs once, on a schedule (CronOperation), or when a resource changes
(WatchOperation). There it sets the `lifecycle.fn.crossplane.io/mode`
annotation on every resource the Operation requires. The composition step then
applies that mode the next time each XR reconciles. Use it to freeze many XRs
at once, for example every weekend:

```yaml
apiVersion: ops.crossplane.io/v1alpha1
kind: CronOperation
metadata:
  name: weekend-freeze
spec:
  schedule: "0 17 * * 5"
  operationTemplate:
    spec:
      mode: Pipeline
      pipeline:
      - step: freeze
        functionRef:
          name: function-lifecycle
        input:
          apiVersion: lifecycle.fn.crossplane.io/v1beta1
          kind: Input
          mode: ObserveOnly
        requirements:
          requiredResources:
          - requirementName: apps
            apiVersion: example.crossplane.io/v1
            kind: App
            matchLabels:
              freeze: "true"
```

* In an Operation the input's `mode` is required. There's no XR to read a mode
  from, so `modeRef` isn't used.
* A second CronOperation with `mode: Full` and a Monday schedule thaws the XRs.
* Operations are alpha. Start Crossplane with `--enable-operations`.

## Developing

| Command | What it does |
|---|---|
| `go generate ./...` | Generates the input CRD. See `input/generate.go`. |
| `go test ./...` | Runs the unit tests in `fn_test.go` and `operation_test.go`. |
| `./example/test.sh` | Renders every example and reports PASS/FAIL. |
| `./e2e/cluster.sh` | Creates a local kind cluster for the e2e test. Run it once. |
| `./e2e/run.sh` | Builds the function from your working tree, installs it on that cluster, and runs the e2e test. |

`./example/test.sh` doesn't need Docker. It renders against the function and
function-patch-and-transform running as local processes, with a Crossplane
binary as the render engine. [example/README.md](example/README.md) shows how
to start them.

The e2e test needs Docker, kind, kubectl, and helm. `./e2e/cluster.sh` creates
a kind cluster with Crossplane (Operations enabled), provider-kubernetes, and a
local registry. `./e2e/run.sh` builds your function, installs it from that
registry, and checks two things render can't: that deleting a Provision XR
leaves its resources behind, and that an Operation freezes the XRs it selects.

Building the runtime image and the package needs Docker with BuildKit:

```shell
docker build . --tag=runtime
crossplane xpkg build -f package --embed-runtime-image=runtime
```

## Related

* [crossplane/crossplane#7367][pr7367] proposes `managementPoliciesOptions`,
  an XR-level switch to orphan resources on delete.

[functions]: https://docs.crossplane.io/latest/composition/compositions/
[policies]: https://docs.crossplane.io/latest/managed-resources/managed-resources/#managementpolicies
[envconfigs]: https://github.com/crossplane-contrib/function-environment-configs
[pr7367]: https://github.com/crossplane/crossplane/pull/7367
[operations]: https://docs.crossplane.io/latest/operations/operation/
