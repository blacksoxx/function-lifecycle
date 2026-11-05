# Example manifests

A two-step pipeline: `function-patch-and-transform` composes a Bucket, a
provider-kubernetes Object, and a plain ConfigMap. `function-lifecycle` then
sets `spec.managementPolicies` on the two managed resources.

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
| `xr.yaml` | none (Full) | `["*"]` |
| `xr-provision.yaml` | Provision | `[Observe, Create, Update, LateInitialize]` |
| `xr-observe-only.yaml` | ObserveOnly | `[Observe]` |
| `xr-deprovision.yaml` | Deprovision | `[Observe, Create, Delete]` |
| `xr-invalid.yaml` | Orphan | fatal result, nothing rendered |
