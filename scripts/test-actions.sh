#!/usr/bin/env bash
# Prueba de extremo a extremo del canal de ÓRDENES (operar desde la GUI). Levanta
# kind, corre control plane + agente (modo kube), y encola una acción de escalado
# como haría la GUI (POST /v1/clusters/{id}/actions). Verifica que:
#   1) el deployment escala DE VERDAD en el clúster,
#   2) la acción termina en estado 'done',
#   3) un reinicio deja la anotación de rollout.
# Limpia todo al terminar.
#
# Requisitos: docker, kind, kubectl, go 1.22+, curl.
#   ./scripts/test-actions.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER=atlas-act
WORKDIR="$(mktemp -d)"
CP_PID=""; AGENT_PID=""

log()  { printf '\033[1;36m▶ %s\033[0m\n' "$*"; }
ok()   { printf '\033[1;32m✓ %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

cleanup() {
  [ -n "$AGENT_PID" ] && kill "$AGENT_PID" 2>/dev/null || true
  [ -n "$CP_PID" ]    && kill "$CP_PID"    2>/dev/null || true
  kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for bin in docker kind kubectl go curl; do
  command -v "$bin" >/dev/null || fail "falta '$bin' en el PATH"
done

log "creando clúster kind + deployment web(2)…"
cat > "$WORKDIR/kind.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: $CLUSTER
nodes: [ { role: control-plane }, { role: worker } ]
EOF
kind create cluster --config "$WORKDIR/kind.yaml" --wait 120s >/dev/null
export KUBECONFIG="$HOME/.kube/config"
kubectl create deployment web --image=nginx:alpine --replicas=2 >/dev/null
kubectl rollout status deployment/web --timeout=120s >/dev/null

log "arrancando control plane + agente (kube)…"
cd "$ROOT"
go build -o "$WORKDIR/controlplane" ./cmd/controlplane
go build -o "$WORKDIR/agent" ./cmd/agent
PORT=39500; while ss -ltn 2>/dev/null | grep -q ":$PORT "; do PORT=$((PORT+1)); done
ATLAS_ADDR=":$PORT" "$WORKDIR/controlplane" >"$WORKDIR/cp.log" 2>&1 &
CP_PID=$!
sleep 1
"$WORKDIR/agent" --collector kube --name "kind act" --provider onprem --cluster-id "$CLUSTER" \
  --control-plane "http://localhost:$PORT" >"$WORKDIR/agent.log" 2>&1 &
AGENT_PID=$!
for _ in $(seq 1 10); do sleep 1; curl -sf "http://localhost:$PORT/v1/topology" | grep -q "$CLUSTER" && break; done

action_status() { curl -s "http://localhost:$PORT/v1/clusters/$CLUSTER/actions" | python3 -c "import sys,json;a=[x for x in json.load(sys.stdin) if x['id']=='$1'];print(a[0]['status'] if a else '?')"; }

log "prueba 1: escalar web 2 -> 5 (como hace la GUI)…"
ID=$(curl -s -X POST "http://localhost:$PORT/v1/clusters/$CLUSTER/actions" -H 'Content-Type: application/json' \
  -d '{"kind":"scale","namespace":"default","workload":"web","workloadKind":"Deployment","replicas":5}' \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
for _ in $(seq 1 15); do sleep 2; [ "$(action_status "$ID")" = "done" ] && break; done
[ "$(action_status "$ID")" = "done" ] || fail "la acción de escalado no llegó a 'done'"
REP=$(kubectl get deploy web -o jsonpath='{.spec.replicas}')
[ "$REP" = "5" ] || fail "el deployment no escaló (réplicas=$REP, esperaba 5)"
ok "web escaló a 5 en el clúster real y la acción está 'done'"

log "prueba 2: reiniciar web (rollout)…"
ID2=$(curl -s -X POST "http://localhost:$PORT/v1/clusters/$CLUSTER/actions" -H 'Content-Type: application/json' \
  -d '{"kind":"restart","namespace":"default","workload":"web","workloadKind":"Deployment"}' \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
for _ in $(seq 1 15); do sleep 2; [ "$(action_status "$ID2")" = "done" ] && break; done
[ "$(action_status "$ID2")" = "done" ] || fail "la acción de reinicio no llegó a 'done'"
ANN=$(kubectl get deploy web -o jsonpath='{.spec.template.metadata.annotations.atlas\.dev/restartedAt}')
[ -n "$ANN" ] || fail "el reinicio no dejó la anotación de rollout"
ok "reinicio aplicado (rollout @ $ANN)"

printf '\n\033[1;32m═══════════════════════════════════════════════\n'
printf '  TODO OK — operar cargas desde la GUI, verificado E2E\n'
printf '═══════════════════════════════════════════════\033[0m\n'
