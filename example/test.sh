#!/usr/bin/env bash
# Renders every example and checks the output. Start both functions first, from
# the repository root, each in its own terminal:
#
#   go run . --insecure --debug --address=127.0.0.1:9443
#   function-patch-and-transform --insecure --address=127.0.0.1:9444
#
# Then run ./example/test.sh. It tests whatever is running on port 9443, so
# restart the function after you change its code.
set -uo pipefail
cd "$(dirname "$0")"

ENGINE=$(command -v crossplane-core) || { echo "crossplane-core isn't on your PATH."; exit 1; }
ENV='apiextensions.crossplane.io/environment={"managementMode":"Deprovision"}'

# name|xr|composition|context|expected text in the output (! means render must fail)
EXAMPLES="
default|xr.yaml|composition.yaml||Full (from input mode): set managementPolicies [*] on 2 managed resources (1 skipped)
provision|xr-provision.yaml|composition.yaml||Provision (from annotation lifecycle.fn.crossplane.io/mode): set managementPolicies [Observe Create Update LateInitialize] on 2 managed resources (1 skipped)
observe-only|xr-observe-only.yaml|composition.yaml||ObserveOnly (from annotation lifecycle.fn.crossplane.io/mode): set managementPolicies [Observe] on 2 managed resources (1 skipped)
deprovision|xr-deprovision.yaml|composition.yaml||Deprovision (from annotation lifecycle.fn.crossplane.io/mode): set managementPolicies [Observe Create Delete] on 2 managed resources (1 skipped)
spec-mode|xr-spec-mode.yaml|composition.yaml||ObserveOnly (from modeRef spec.parameters.managementMode)
environment|xr.yaml|composition-environment.yaml|$ENV|Deprovision (from modeRef context.[apiextensions.crossplane.io/environment].managementMode)
invalid|xr-invalid.yaml|composition.yaml||!unknown mode \"Orphan\"
"

listening() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }

for port in 9443 9444; do
  if ! listening "$port"; then
    echo "Nothing is listening on 127.0.0.1:$port. Start both functions first; the top of this script shows how."
    exit 1
  fi
done

fail=0
while IFS='|' read -r name xr comp context expect; do
  [ -z "$name" ] && continue
  out=/tmp/render-$name.out
  args=("$xr" "$comp" functions.yaml -r --crossplane-binary="$ENGINE")
  [ -n "$context" ] && args+=(--context-values="$context")
  crossplane composition render "${args[@]}" >"$out" 2>&1
  rc=$?
  # YAML folds long messages across lines. Squash whitespace before matching.
  flat=$(tr -s ' \n' '  ' <"$out")
  if [[ $expect == !* ]]; then
    [[ $rc -ne 0 && $flat == *"${expect#!}"* ]] && ok=1 || ok=0
  else
    [[ $rc -eq 0 && $flat == *"$expect"* ]] && ok=1 || ok=0
  fi
  if [ $ok -eq 1 ]; then echo "PASS  $name"; else echo "FAIL  $name (see $out)"; fail=1; fi
done <<<"$EXAMPLES"

# The Operation sets the mode annotation on the two Apps in apps.yaml labelled
# freeze: "true".
out=/tmp/render-operation.out
crossplane operation render operation.yaml functions.yaml -e apps.yaml -r --crossplane-binary="$ENGINE" >"$out" 2>&1
rc=$?
flat=$(tr -s ' \n' '  ' <"$out")
if [[ $rc -eq 0 && $flat == *"Set lifecycle.fn.crossplane.io/mode to ObserveOnly on 2 resources"* ]]; then
  echo "PASS  operation"
else
  echo "FAIL  operation (see $out)"
  fail=1
fi
exit $fail
