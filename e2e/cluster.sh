#!/usr/bin/env bash
# Creates the control plane for the e2e test: a kind cluster with Crossplane
# (Operations enabled), provider-kubernetes, function-patch-and-transform,
# function-auto-ready, and the App API, plus a local registry for your
# function's package. Run it from the repository root, then ./e2e/run.sh:
#
#   ./e2e/cluster.sh
#
# The first run pulls about 1.5 GB of images. Running it again is safe. To
# delete everything:
#
#   kind delete cluster --name lifecycle-e2e && docker rm -f lifecycle-e2e-registry
set -euo pipefail
cd "$(dirname "$0")/.."

CLUSTER=${CLUSTER:-lifecycle-e2e}
REGISTRY_PORT=${REGISTRY_PORT:-5001}
CROSSPLANE_VERSION=2.4.3

step() { printf '\n==> %s\n' "$*"; }

step "kind cluster $CLUSTER"
kind get clusters 2>/dev/null | grep -qx "$CLUSTER" || kind create cluster --name "$CLUSTER" --wait 2m
kind export kubeconfig --name "$CLUSTER"

step "Local registry $CLUSTER-registry, on kind's network and on localhost:$REGISTRY_PORT"
docker inspect "$CLUSTER-registry" >/dev/null 2>&1 ||
  docker run -d --name "$CLUSTER-registry" --network kind -p "127.0.0.1:$REGISTRY_PORT:5000" registry:2

step "Crossplane $CROSSPLANE_VERSION, with Operations enabled"
helm repo add crossplane-stable https://charts.crossplane.io/stable --force-update
helm upgrade --install crossplane crossplane-stable/crossplane --version "$CROSSPLANE_VERSION" \
  --namespace crossplane-system --create-namespace --set 'args={--enable-operations}' --wait

step "provider-kubernetes, function-patch-and-transform, function-auto-ready"
kubectl apply -f e2e/manifests/provider.yaml -f e2e/manifests/functions.yaml
kubectl wait providers.pkg.crossplane.io,functions.pkg.crossplane.io --all --for condition=Healthy --timeout 5m

step "The App API: XRD, Composition, and the provider's ProviderConfig"
kubectl apply -f e2e/manifests/definition.yaml
kubectl wait xrd apps.example.crossplane.io --for condition=Established --timeout 2m
kubectl apply -f e2e/manifests/composition.yaml -f e2e/manifests/providerconfig.yaml

echo
echo "The control plane is ready. Next: ./e2e/run.sh"
