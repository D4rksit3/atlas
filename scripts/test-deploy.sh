#!/usr/bin/env bash
# Prueba de extremo a extremo del DESPLIEGUE de Atlas DENTRO de Kubernetes.
# Construye las 3 imágenes, las carga en un clúster kind, aplica los manifiestos
# (control plane + GUI + agente in-cluster) y verifica que el circuito completo
# funciona: el agente registra el clúster y la topología se sirve a través de la
# GUI. Atlas termina monitoreándose a sí mismo. Limpia todo al terminar.
#
# Requisitos: docker, kind, kubectl, go 1.22+.
#   ./scripts/test-deploy.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER=atlas-deploy
WORKDIR="$(mktemp -d)"
PF_PID=""

log()  { printf '\033[1;36m▶ %s\033[0m\n' "$*"; }
ok()   { printf '\033[1;32m✓ %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

cleanup() {
  [ -n "$PF_PID" ] && kill "$PF_PID" 2>/dev/null || true
  kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for bin in docker kind kubectl go; do
  command -v "$bin" >/dev/null || fail "falta '$bin' en el PATH"
done

log "construyendo imágenes (controlplane, agent, web)…"
docker build -q -t atlas-controlplane:dev --target controlplane "$ROOT" >/dev/null
docker build -q -t atlas-agent:dev        --target agent        "$ROOT" >/dev/null
docker build -q -t atlas-web:dev -f "$ROOT/web/Dockerfile"      "$ROOT" >/dev/null
ok "imágenes construidas"

log "creando clúster kind y cargando imágenes…"
cat > "$WORKDIR/kind.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: $CLUSTER
nodes:
  - role: control-plane
  - role: worker
EOF
kind create cluster --config "$WORKDIR/kind.yaml" --wait 120s >/dev/null
export KUBECONFIG="$HOME/.kube/config"
kind load docker-image atlas-controlplane:dev atlas-web:dev atlas-agent:dev --name "$CLUSTER" >/dev/null

log "aplicando manifiestos (imágenes :dev, control plane in-cluster)…"
sed -e 's#ghcr.io/atlasctl/atlas-controlplane:latest#atlas-controlplane:dev#' \
    -e 's#image: atlas-controlplane:dev#image: atlas-controlplane:dev\n          imagePullPolicy: IfNotPresent#' \
    "$ROOT/deploy/controlplane.yaml" > "$WORKDIR/cp.yaml"
sed -e 's#ghcr.io/atlasctl/atlas-web:latest#atlas-web:dev#' \
    -e 's#image: atlas-web:dev#image: atlas-web:dev\n          imagePullPolicy: IfNotPresent#' \
    "$ROOT/deploy/web.yaml" > "$WORKDIR/web.yaml"
sed -e 's#ghcr.io/atlasctl/atlas-agent:latest#atlas-agent:dev#' \
    -e 's#image: atlas-agent:dev#image: atlas-agent:dev\n          imagePullPolicy: IfNotPresent#' \
    -e 's#https://atlas-controlplane.example.com#http://atlas-controlplane.atlas-system:8080#' \
    -e 's/value: "mi-cluster"/value: "in-cluster demo"/' \
    "$ROOT/deploy/agent.yaml" > "$WORKDIR/agent.yaml"
kubectl apply -f "$WORKDIR/cp.yaml" -f "$WORKDIR/web.yaml" -f "$WORKDIR/agent.yaml" >/dev/null

kubectl -n atlas-system rollout status deploy/atlas-controlplane --timeout=120s >/dev/null
kubectl -n atlas-system rollout status deploy/atlas-web --timeout=120s >/dev/null
kubectl -n atlas-system rollout status deploy/atlas-agent --timeout=120s >/dev/null
ok "control plane + GUI + agente corriendo dentro del clúster"

log "verificando el circuito (agente → control plane ← GUI)…"
kubectl -n atlas-system port-forward svc/atlas-web 8088:80 >/dev/null 2>&1 &
PF_PID=$!
ONLINE=""
for _ in $(seq 1 20); do
  sleep 3
  ONLINE=$(curl -s http://localhost:8088/v1/topology | python3 -c "import sys,json;d=json.load(sys.stdin);print(d['clusters'][0]['online'] if d['clusters'] else False)" 2>/dev/null || echo False)
  [ "$ONLINE" = "True" ] && break
done
[ "$ONLINE" = "True" ] || fail "el clúster no aparece online en la topología"

echo ""; echo "  Atlas se ve a sí mismo (cargas con su ubicación):"
curl -s http://localhost:8088/v1/topology | python3 -c "
import sys,json
c=json.load(sys.stdin)['clusters'][0]
for w in c['snapshot']['workloads']:
    if w['name'].startswith('atlas-') and w.get('placement'):
        print(f\"    {w['name']} -> \" + ', '.join(f\"{p['node']}×{p['pods']}\" for p in w['placement']))"

SELF=$(curl -s http://localhost:8088/v1/topology | python3 -c "import sys,json;print(sum(1 for w in json.load(sys.stdin)['clusters'][0]['snapshot']['workloads'] if w['name'].startswith('atlas-')))")
[ "$SELF" -ge 3 ] || fail "esperaba ver los 3 componentes de Atlas como cargas, vi $SELF"
ok "Atlas monitorea sus propios componentes ($SELF cargas atlas-*)"

printf '\n\033[1;32m═══════════════════════════════════════════════\n'
printf '  TODO OK — Atlas desplegado en K8s, verificado E2E\n'
printf '═══════════════════════════════════════════════\033[0m\n'
