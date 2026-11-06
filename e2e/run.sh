#!/usr/bin/env bash
# The e2e test. It builds the function from your working tree, installs it on
# the control plane ./e2e/cluster.sh created, and checks two things render
# can't: deleting a Provision XR leaves its resources behind, and an Operation
# freezes only the XRs it selects. Run it from the repository root:
#
#   ./e2e/cluster.sh   # once
#   ./e2e/run.sh       # after every change
#
# The cluster stays up, so you can look around afterwards.
set -euo pipefail
cd "$(dirname "$0")/.."

CLUSTER=${CLUSTER:-lifecycle-e2e}
REGISTRY_PORT=${REGISTRY_PORT:-5001}
TAG=e2e-$(date +%s)
BUILD=$(mktemp -d)
trap 'rm -rf "$BUILD"' EXIT

for tool in docker kind kubectl crossplane go; do
  command -v "$tool" >/dev/null || { echo "$tool isn't on your PATH."; exit 1; }
done
kind get clusters 2>/dev/null | grep -qx "$CLUSTER" || { echo "There's no $CLUSTER cluster. Run ./e2e/cluster.sh first."; exit 1; }
kind export kubeconfig --name "$CLUSTER" >/dev/null

step() { printf '\n==> %s\n' "$*"; }

# wait_for SECONDS COMMAND...: retries COMMAND until it succeeds, or fails
# after SECONDS.
wait_for() {
  local timeout=$1
  shift
  for ((i = 0; i < timeout; i += 2)); do
    "$@" >/dev/null 2>&1 && return 0
    sleep 2
  done
  return 1
}

# is CMD... EXPECTED: succeeds if the command prints exactly EXPECTED.
is() {
  local want=${*: -1}
  [[ $("${@:1:$#-1}" 2>/dev/null) == "$want" ]]
}

app() { kubectl get apps.example.crossplane.io "$@" -n default; }
policies() {
  kubectl get objects.kubernetes.m.crossplane.io -n default -l "crossplane.io/composite=$1" \
    -o jsonpath='{.items[0].spec.managementPolicies}'
}

step "Build function-lifecycle:$TAG from your working tree, and load it into kind"
CGO_ENABLED=0 GOOS=linux GOARCH=$(docker version --format '{{.Server.Arch}}') go build -o "$BUILD/function" .
cp e2e/Dockerfile "$BUILD/"
docker build -q -t "function-lifecycle:$TAG" "$BUILD" >/dev/null
kind load docker-image "function-lifecycle:$TAG" --name "$CLUSTER" >/dev/null

step "Build the package, and push it to the local registry"
crossplane xpkg build -f package -o "$BUILD/function.xpkg" >/dev/null
crossplane xpkg push -f "$BUILD/function.xpkg" "localhost:$REGISTRY_PORT/function-lifecycle:$TAG" >/dev/null

step "Install it"
# Crossplane reaches the registry at its IP on kind's network. A registry at a
# private IP address is plain HTTP, so there's no TLS to set up.
REGISTRY=$(docker inspect -f '{{(index .NetworkSettings.Networks "kind").IPAddress}}' "$CLUSTER-registry"):5000
PACKAGE=$REGISTRY/function-lifecycle:$TAG
sed -e "s#FN_PACKAGE#$PACKAGE#" -e "s#FN_IMAGE#function-lifecycle:$TAG#" e2e/manifests/function-lifecycle.yaml | kubectl apply -f - >/dev/null
wait_for 300 is kubectl get functions.pkg.crossplane.io function-lifecycle -o jsonpath='{.status.currentIdentifier}' "$PACKAGE" ||
  { echo "Crossplane didn't install your build within 5 minutes: kubectl describe functions.pkg.crossplane.io function-lifecycle"; exit 1; }
kubectl wait functions.pkg.crossplane.io function-lifecycle --for condition=Healthy --timeout 5m >/dev/null

step "Fresh Apps"
kubectl delete operations.ops.crossplane.io e2e-freeze-apps --ignore-not-found >/dev/null
kubectl delete -f e2e/manifests/apps.yaml --ignore-not-found --wait >/dev/null
kubectl delete configmap -n default provision-config frozen-config free-config --ignore-not-found >/dev/null
kubectl apply -f e2e/manifests/apps.yaml >/dev/null
kubectl wait apps.example.crossplane.io --all -n default --for condition=Ready --timeout 3m >/dev/null

# From here on, a failed check doesn't stop the run.
set +e
fail=0
# check SECONDS DESCRIPTION COMMAND...
check() {
  local timeout=$1 what=$2
  shift 2
  if wait_for "$timeout" "$@"; then echo "PASS  $what"; return 0; fi
  echo "FAIL  $what"
  fail=1
  return 1
}

step "Scenario 1: Provision, then delete the App. Its ConfigMap survives."
kubectl annotate apps.example.crossplane.io provision -n default lifecycle.fn.crossplane.io/mode=Provision >/dev/null
check 60 "Provision sets the Object's policies" \
  is policies provision '["Observe","Create","Update","LateInitialize"]'
kubectl delete apps.example.crossplane.io provision -n default --wait --timeout 2m >/dev/null
check 30 "the ConfigMap outlives the App" kubectl get configmap provision-config -n default

step "Scenario 2: an Operation freezes the Apps labelled freeze: \"true\"."
kubectl apply -f e2e/manifests/operation.yaml >/dev/null
# An Operation retries a failed pipeline, then gives up. Wait for either.
succeeded() { kubectl get operations.ops.crossplane.io e2e-freeze-apps -o jsonpath='{.status.conditions[?(@.type=="Succeeded")].status}'; }
finished() { [[ $(succeeded) == True ]] || kubectl get operations.ops.crossplane.io e2e-freeze-apps -o yaml | grep -q 'failure limit'; }
wait_for 180 finished
if [[ $(succeeded) == True ]]; then
  echo "PASS  the Operation succeeds"
  check 30 "the Operation annotates the labelled App" \
    is app frozen -o jsonpath='{.metadata.annotations.lifecycle\.fn\.crossplane\.io/mode}' ObserveOnly
  check 60 "the composition applies ObserveOnly" is policies frozen '["Observe"]'
else
  echo "FAIL  the Operation succeeds"
  fail=1
  kubectl get operations.ops.crossplane.io e2e-freeze-apps \
    -o jsonpath='{range .status.conditions[?(@.message)]}{"      "}{.type}: {.message}{"\n"}{end}'
fi
check 5 "the other App keeps Full" is policies free '["*"]'

echo
if [ $fail -eq 0 ]; then echo "All e2e checks passed."; else echo "Some e2e checks failed."; fi
echo "The cluster is still up: kubectl get apps,managed -n default"
exit $fail
