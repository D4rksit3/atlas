#!/usr/bin/env bash
# Prueba de extremo a extremo de INSTALAR UN COMPLEMENTO desde la GUI (ArgoCD).
# Levanta un clúster k3d, corre control plane + agente (kube) y encola una acción
# 'install addon=argocd' como haría el botón de la consola. Verifica que:
#   1) la acción llega a 'done',
#   2) el namespace 'argocd' y sus componentes existen en el clúster,
#   3) Atlas ve ArgoCD en su topología (argocd-server).
# Limpia todo al terminar.
#
# Requisitos: docker, k3d, kubectl, go 1.22+, curl. (Descarga el manifiesto de
# ArgoCD v2.11.7 desde GitHub y tira imágenes: necesita internet.)
#   ./scripts/test-argocd.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER=atlas-argo
WORKDIR="$(mktemp -d)"
CP_PID=""; AGENT_PID=""

log()  { printf '\033[1;36m▶ %s\033[0m\n' "$*"; }
ok()   { printf '\033[1;32m✓ %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

cleanup() {
  [ -n "$AGENT_PID" ] && kill "$AGENT_PID" 2>/dev/null || true
  [ -n "$CP_PID" ]    && kill "$CP_PID"    2>/dev/null || true
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for bin in docker k3d kubectl go curl; do
  command -v "$bin" >/dev/null || fail "falta '$bin' en el PATH"
done

log "creando clúster k3d…"
k3d cluster create "$CLUSTER" --agents 1 --wait >/dev/null 2>&1
export KUBECONFIG="$HOME/.kube/config"

log "arrancando control plane + agente (kube)…"
cd "$ROOT"
go build -o "$WORKDIR/controlplane" ./cmd/controlplane
go build -o "$WORKDIR/agent" ./cmd/agent
PORT=39600; while ss -ltn 2>/dev/null | grep -q ":$PORT "; do PORT=$((PORT+1)); done
ATLAS_ADDR=":$PORT" "$WORKDIR/controlplane" >"$WORKDIR/cp.log" 2>&1 &
CP_PID=$!
sleep 1
"$WORKDIR/agent" --collector kube --name "prod k3s" --provider onprem --cluster-id prod-k3s \
  --control-plane "http://localhost:$PORT" >"$WORKDIR/agent.log" 2>&1 &
AGENT_PID=$!
for _ in $(seq 1 10); do sleep 1; curl -sf "http://localhost:$PORT/v1/topology" | grep -q prod-k3s && break; done

log "encolando 'install argocd' (como el botón de la consola)…"
ID=$(curl -s -X POST "http://localhost:$PORT/v1/clusters/prod-k3s/actions" -H 'Content-Type: application/json' \
  -d '{"kind":"install","addon":"argocd"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
status() { curl -s "http://localhost:$PORT/v1/clusters/prod-k3s/actions" | python3 -c "import sys,json;a=[x for x in json.load(sys.stdin) if x['id']=='$1'];print(a[0]['status'] if a else '?')"; }
for _ in $(seq 1 40); do
  sleep 3
  ST=$(status "$ID"); [ "$ST" = "done" ] && break
  [ "$ST" = "error" ] && { curl -s "http://localhost:$PORT/v1/clusters/prod-k3s/actions" | python3 -c "import sys,json;print([a.get('error') for a in json.load(sys.stdin) if a['id']=='$ID'])"; fail "la instalación falló"; }
done
[ "$(status "$ID")" = "done" ] || fail "la acción no llegó a 'done'"
ok "acción install en estado 'done'"

log "verificando ArgoCD en el clúster…"
kubectl get ns argocd >/dev/null 2>&1 || fail "no se creó el namespace argocd"
kubectl -n argocd get deploy argocd-server >/dev/null 2>&1 || fail "no existe argocd-server"
N=$(kubectl -n argocd get deploy --no-headers | wc -l)
ok "ArgoCD instalado: namespace argocd + $N deployments"

log "verificando que Atlas lo ve…"
for _ in $(seq 1 10); do
  sleep 3
  SEEN=$(curl -s "http://localhost:$PORT/v1/topology" | python3 -c "import sys,json;c=json.load(sys.stdin)['clusters'][0];print(any(w['name']=='argocd-server' for w in c['snapshot']['workloads']))" 2>/dev/null)
  [ "$SEEN" = "True" ] && break
done
[ "$SEEN" = "True" ] || fail "Atlas no ve argocd-server en la topología"
ok "Atlas ve ArgoCD en su mapa"

printf '\n\033[1;32m════════════════════════════════════════════\n'
printf '  TODO OK — instalar ArgoCD desde la GUI, E2E\n'
printf '════════════════════════════════════════════\033[0m\n'
