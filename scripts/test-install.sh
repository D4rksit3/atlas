#!/usr/bin/env bash
# Verifica el instalador E2E: levanta un k3d, instala Atlas en modo LOCAL con un
# dominio, y comprueba que el Ingress enruta la GUI y la API por ese dominio.
set -euo pipefail
cd "$(dirname "$0")/.."
CLUSTER="atlas-install-test" PORT=8088 DOMAIN="atlas.local"
export KUBECONFIG="${KUBECONFIG:-$HOME/.kube/config}"

cleanup() { k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "== build imágenes (dev) =="
docker build -q -t atlas-controlplane:dev --target controlplane . >/dev/null
docker build -q -t atlas-web:dev -f web/Dockerfile . >/dev/null
docker build -q -t atlas-agent:dev --target agent . >/dev/null

echo "== k3d $CLUSTER ($PORT:80) =="
cleanup
k3d cluster create "$CLUSTER" --agents 0 -p "$PORT:80@loadbalancer" --wait >/dev/null
kubectl wait --for=condition=Ready nodes --all --timeout=60s >/dev/null
k3d image import atlas-controlplane:dev atlas-web:dev atlas-agent:dev -c "$CLUSTER" >/dev/null

echo "== instalar (local, $DOMAIN) =="
./scripts/install.sh --domain "$DOMAIN" --mode local --image-prefix "" --tag dev >/dev/null
kubectl -n atlas-system rollout status deploy/atlas-controlplane --timeout=90s >/dev/null
kubectl -n atlas-system rollout status deploy/atlas-web --timeout=90s >/dev/null

echo "== comprobaciones =="
fail=0
check() { # descripción, esperado, obtenido
  if [ "$2" = "$3" ]; then echo "  ✓ $1 ($3)"; else echo "  ✗ $1: esperado $2, obtenido $3"; fail=1; fi
}
sleep 3
gui=$(curl -s -o /dev/null -w '%{http_code}' --resolve "$DOMAIN:$PORT:127.0.0.1" "http://$DOMAIN:$PORT/")
api=$(curl -s -o /dev/null -w '%{http_code}' --resolve "$DOMAIN:$PORT:127.0.0.1" "http://$DOMAIN:$PORT/v1/authconfig")
other=$(curl -s -o /dev/null -w '%{http_code}' --resolve "otro.local:$PORT:127.0.0.1" "http://otro.local:$PORT/")
cors=$(kubectl -n atlas-system get deploy atlas-controlplane -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="ATLAS_CORS_ORIGIN")].value}')
check "GUI por el dominio" 200 "$gui"
check "API /v1 por el dominio" 200 "$api"
check "host desconocido -> 404" 404 "$other"
check "CORS del control plane" "http://$DOMAIN" "$cors"

[ "$fail" = 0 ] && echo "== OK ==" || { echo "== FALLO =="; exit 1; }
