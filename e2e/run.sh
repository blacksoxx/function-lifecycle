#!/usr/bin/env bash
# Tests the function end to end on a local kind cluster: Crossplane with
# Operations enabled, provider-kubernetes, and this function built from your
# working tree. Run it from the repository root:
#
#   ./e2e/run.sh
#
# The first run takes a few minutes, most of it pulling images. Later runs reuse
# the cluster and only rebuild the function. The cluster stays up so you can
# look around. To delete it:
#
#   kind delete cluster --name lifecycle-e2e && docker rm -f lifecycle-e2e-registry
set -euo pipefail
cd "$(dirname "$0")/.."

CLUSTER=lifecycle-e2e
REGISTRY=$CLUSTER-registry
CROSSPLANE_VERSION=2.4.3
TAG=e2e-$(date +%s)
BUILD=$(mktemp -d)
trap 'rm -rf "$BUILD"' EXIT

for tool in docker kind kubectl helm crossplane go; do
  command -v "$tool" >/dev/null || { echo "$tool isn't on your PATH."; exit 1; }
done

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

step "Cluster $CLUSTER"
kind get clusters 2>/dev/null | grep -qx "$CLUSTER" || kind create cluster --name "$CLUSTER" --wait 2m
kind export kubeconfig --name "$CLUSTER" >/dev/null

step "Local registry"
if ! docker inspect "$REGISTRY" >/dev/null 2>&1; then
  docker run -d --name "$REGISTRY" --network kind -p 127.0.0.1:5001:5000 registry:2 >/dev/null
fi
# Crossplane reaches the registry at its IP on kind's network. A registry at a
# private IP address is plain HTTP, so there's no TLS to set up.
REGISTRY_IP=$(docker inspect -f '{{(index .NetworkSettings.Networks "kind").IPAddress}}' "$REGISTRY")

step "Crossplane $CROSSPLANE_VERSION, with Operations enabled"
helm repo add crossplane-stable https://charts.crossplane.io/stable --force-update >/dev/null
helm upgrade --install crossplane crossplane-stable/crossplane --version "$CROSSPLANE_VERSION" \
  --namespace crossplane-system --create-namespace --set 'args={--enable-operations}' --wait >/dev/null

step "Build the function from your working tree"
# A plain go build and a two-line image work with any Docker, BuildKit or not.
ARCH=$(docker version --format '{{.Server.Arch}}')
CGO_ENABLED=0 GOOS=linux GOARCH=$ARCH go build -o "$BUILD/function" .
printf 'FROM gcr.io/distroless/static-debian12:nonroot\nCOPY function /function\nUSER nonroot:nonroot\nENTRYPOINT ["/function"]\n' >"$BUILD/Dockerfile"
docker build -q -t "function-lifecycle:$TAG" "$BUILD" >/dev/null
kind load docker-image "function-lifecycle:$TAG" --name "$CLUSTER" >/dev/null
crossplane xpkg build -f package -o "$BUILD/function.xpkg" >/dev/null
crossplane xpkg push -f "$BUILD/function.xpkg" "localhost:5001/function-lifecycle:$TAG" >/dev/null
PACKAGE=$REGISTRY_IP:5000/function-lifecycle:$TAG

step "Provider, functions, and the App API"
kubectl apply -f e2e/manifests/provider.yaml >/dev/null
sed -e "s#FN_PACKAGE#$PACKAGE#" -e "s#FN_IMAGE#function-lifecycle:$TAG#" e2e/manifests/functions.yaml | kubectl apply -f - >/dev/null
wait_for 300 is kubectl get functions.pkg.crossplane.io function-lifecycle -o jsonpath='{.status.currentIdentifier}' "$PACKAGE" ||
  { echo "Crossplane didn't install your build within 5 minutes: kubectl describe functions.pkg.crossplane.io function-lifecycle"; exit 1; }
kubectl wait providers.pkg.crossplane.io --all --for condition=Healthy --timeout 5m >/dev/null
kubectl wait functions.pkg.crossplane.io --all --for condition=Healthy --timeout 5m >/dev/null
kubectl apply -f e2e/manifests/providerconfig.yaml -f e2e/manifests/definition.yaml >/dev/null
kubectl wait xrd apps.example.crossplane.io --for condition=Established --timeout 2m >/dev/null
kubectl apply -f e2e/manifests/composition.yaml >/dev/null

step "Fresh Apps"
kubectl delete operations.ops.crossplane.io freeze-apps --ignore-not-found >/dev/null
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
succeeded() { kubectl get operations.ops.crossplane.io freeze-apps -o jsonpath='{.status.conditions[?(@.type=="Succeeded")].status}'; }
finished() { [[ $(succeeded) == True ]] || kubectl get operations.ops.crossplane.io freeze-apps -o yaml | grep -q 'failure limit'; }
wait_for 180 finished
if [[ $(succeeded) == True ]]; then
  echo "PASS  the Operation succeeds"
  check 30 "the Operation annotates the labelled App" \
    is app frozen -o jsonpath='{.metadata.annotations.lifecycle\.fn\.crossplane\.io/mode}' ObserveOnly
  check 60 "the composition applies ObserveOnly" is policies frozen '["Observe"]'
else
  echo "FAIL  the Operation succeeds"
  fail=1
  kubectl get operations.ops.crossplane.io freeze-apps \
    -o jsonpath='{range .status.conditions[?(@.message)]}{"      "}{.type}: {.message}{"\n"}{end}'
fi
check 5 "the other App keeps Full" is policies free '["*"]'

echo
if [ $fail -eq 0 ]; then echo "All e2e checks passed."; else echo "Some e2e checks failed."; fi
echo "The cluster is still up: kubectl get apps,managed -n default"
exit $fail
