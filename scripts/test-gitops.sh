#!/usr/bin/env bash
# Prueba de extremo a extremo del flujo GitOps completo desde la GUI:
#   1) instalar ArgoCD (acción install),
#   2) registrar un PROYECTO (acción addapp) apuntando a un repo Git público,
#   3) ArgoCD lo sincroniza y Atlas ve el proyecto (Synced) y sus cargas.
# Usa el repo de ejemplo de ArgoCD (guestbook). Limpia todo al terminar.
#
# Requisitos: docker, k3d, kubectl, go 1.22+, curl, internet.
#   ./scripts/test-gitops.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER=atlas-gitops
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

log "creando clúster k3d + control plane + agente…"
k3d cluster create "$CLUSTER" --agents 1 --wait >/dev/null 2>&1
export KUBECONFIG="$HOME/.kube/config"
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

post() { curl -s -X POST "http://localhost:$PORT/v1/clusters/prod-k3s/actions" -H 'Content-Type: application/json' -d "$1" | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])"; }
status() { curl -s "http://localhost:$PORT/v1/clusters/prod-k3s/actions" | python3 -c "import sys,json;a=[x for x in json.load(sys.stdin) if x['id']=='$1'];print(a[0]['status'] if a else '?')"; }
wait_done() { for _ in $(seq 1 40); do sleep 3; s=$(status "$1"); [ "$s" = "done" ] && return 0; [ "$s" = "error" ] && return 1; done; return 1; }

log "1) instalar ArgoCD (acción install)…"
ID=$(post '{"kind":"install","addon":"argocd"}')
wait_done "$ID" || fail "la instalación de ArgoCD falló"
kubectl -n argocd rollout status deploy/argocd-server --timeout=150s >/dev/null || fail "argocd-server no arrancó"
kubectl -n argocd rollout status deploy/argocd-repo-server --timeout=150s >/dev/null || true
ok "ArgoCD instalado y listo"

log "2) registrar un proyecto GitOps (acción addapp → guestbook)…"
ID2=$(post '{"kind":"addapp","app":{"name":"guestbook","repoURL":"https://github.com/argoproj/argocd-example-apps","path":"guestbook","namespace":"default"}}')
wait_done "$ID2" || fail "no se pudo registrar el proyecto"
kubectl -n argocd get application guestbook >/dev/null 2>&1 || fail "no se creó la Application 'guestbook'"
ok "proyecto 'guestbook' registrado (Application creada)"

log "3) esperando a que ArgoCD sincronice y Atlas lo vea…"
SEEN=""
for _ in $(seq 1 40); do
  sleep 5
  SEEN=$(curl -s "http://localhost:$PORT/v1/topology" | python3 -c "
import sys,json
c=json.load(sys.stdin)['clusters'][0]
a=[x for x in (c['snapshot'].get('apps') or []) if x['name']=='guestbook']
print(a[0]['sync'] if a else '')" 2>/dev/null)
  echo "  sync de guestbook según Atlas: ${SEEN:-(aún no)}"
  [ "$SEEN" = "Synced" ] && break
done
[ "$SEEN" = "Synced" ] || fail "el proyecto no llegó a Synced en Atlas"
ok "Atlas ve el proyecto 'guestbook' en estado Synced"

# y sus cargas desplegadas por GitOps aparecen
kubectl -n default get deploy guestbook-ui >/dev/null 2>&1 && ok "las cargas del repo (guestbook-ui) se desplegaron solas" || true

printf '\n\033[1;32m═══════════════════════════════════════════════\n'
printf '  TODO OK — GitOps desde la GUI: proyecto → sync → mapa\n'
printf '═══════════════════════════════════════════════\033[0m\n'
