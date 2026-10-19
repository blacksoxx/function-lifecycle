# Example manifests

A two-step pipeline:

1. `function-patch-and-transform` composes a Bucket, a provider-kubernetes
   Object, and a plain ConfigMap.
2. `function-lifecycle` sets `spec.managementPolicies` on the two managed
   resources and skips the ConfigMap.

Render runs without Docker: both functions run as local processes, and a
Crossplane binary is the render engine. Use three terminals, all in the
repository root.

Run the function:

```shell
go run . --insecure --debug --address=127.0.0.1:9443
```

In a second terminal, run function-patch-and-transform. Install it with
`go install github.com/crossplane-contrib/function-patch-and-transform@v0.11.2`.

```shell
function-patch-and-transform --insecure --address=127.0.0.1:9444
```

In a third terminal, render an XR. Pick any `xr*.yaml` file:

```shell
crossplane composition render example/xr-provision.yaml example/composition.yaml \
  example/functions.yaml -r --crossplane-binary=$(which crossplane-core)
```

`--crossplane-binary` takes the path to a Crossplane binary, here installed as
`crossplane-core` from
`https://releases.crossplane.io/stable/v2.4.3/bin/<os>_<arch>/crossplane`.
Without it, render runs Crossplane in a Docker container. `-r` prints each
step's results.

| XR | Mode | Policies on managed resources |
|---|---|---|
| `xr.yaml` | Full, from the input's `mode` | `["*"]` |
| `xr-provision.yaml` | Provision, from the annotation | `[Observe, Create, Update, LateInitialize]` |
| `xr-observe-only.yaml` | ObserveOnly, from the annotation | `[Observe]` |
| `xr-deprovision.yaml` | Deprovision, from the annotation | `[Observe, Create, Delete]` |
| `xr-spec-mode.yaml` | ObserveOnly, from `spec.parameters.managementMode` via `modeRef` | `[Observe]` |
| `xr-invalid.yaml` | Orphan | fatal result, nothing rendered |

The annotation always wins. Without it, `modeRef` is read, then the input's
`mode`, then `Full`.

To drive the mode from the pipeline context, for example an EnvironmentConfig
loaded by function-environment-configs, use `composition-environment.yaml`:

```shell
crossplane composition render example/xr.yaml example/composition-environment.yaml \
  example/functions.yaml -r --crossplane-binary=$(which crossplane-core) \
  --context-values='apiextensions.crossplane.io/environment={"managementMode":"Deprovision"}'
```

`--context-values` fakes what function-environment-configs would put in the
context, so you can try this without a cluster.

## Run it as an Operation

`operation.yaml` sets the mode annotation on the Apps in `apps.yaml` that are
labelled `freeze: "true"`. Only the function in terminal 1 needs to run:

```shell
crossplane operation render example/operation.yaml example/functions.yaml \
  -e example/apps.yaml -r --crossplane-binary=$(which crossplane-core)
```

`-e` passes the Apps in `apps.yaml` as if they were in the cluster. The output
shows the Operation, with the two Apps it changed in
`status.appliedResourceRefs`, then the patch it applies to each.
`cronoperation.yaml` renders the same way.

## Test every example

With both functions running, render every example and check the output:

```shell
./example/test.sh
```

It prints `PASS` or `FAIL` for each example. It tests whatever is running on
port 9443, so restart the function after you change its code.

## Debugging

* `--include-function-results` (`-r`) prints each step's results.
* `--include-context` (`-c`) prints the pipeline context as `kind: Context`.
* `--include-full-xr` (`-x`) adds the input XR's spec and metadata to the
  rendered XR. The `LifecycleMode` condition is in its status either way.
* `--debug` on the function logs each resource it changes or skips.

For example:

```shell
crossplane composition render example/xr-observe-only.yaml example/composition.yaml \
  example/functions.yaml -r -x --crossplane-binary=$(which crossplane-core)
```
