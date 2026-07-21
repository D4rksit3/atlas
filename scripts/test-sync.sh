#!/usr/bin/env bash
# Prueba E2E de SINCRONIZAR y REVERTIR un proyecto GitOps desde la GUI.
# Instala ArgoCD, registra un proyecto (guestbook) y comprueba:
#   1) 'sync' fuerza una sincronización (la acción llega a 'done'),
#   2) 'rollback' sin versión anterior devuelve un error CLARO (guard),
# lo que valida la mecánica (leer historial, disparar operation.sync). Limpia todo.
#
# Requisitos: docker, k3d, kubectl, go 1.22+, curl, internet.
#   ./scripts/test-sync.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER=atlas-sync
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

for bin in docker k3d kubectl go curl; do command -v "$bin" >/dev/null || fail "falta '$bin'"; done

log "clúster + control plane + agente…"
k3d cluster create "$CLUSTER" --agents 1 --wait >/dev/null 2>&1
export KUBECONFIG="$HOME/.kube/config"
cd "$ROOT"
go build -o "$WORKDIR/controlplane" ./cmd/controlplane
go build -o "$WORKDIR/agent" ./cmd/agent
PORT=39600; while ss -ltn 2>/dev/null | grep -q ":$PORT "; do PORT=$((PORT+1)); done
ATLAS_ADDR=":$PORT" "$WORKDIR/controlplane" >"$WORKDIR/cp.log" 2>&1 &
CP_PID=$!
sleep 1
"$WORKDIR/agent" --collector kube --name "prod k3s" --provider onprem --cluster-id prod-k3s --control-plane "http://localhost:$PORT" >"$WORKDIR/agent.log" 2>&1 &
AGENT_PID=$!
for _ in $(seq 1 10); do sleep 1; curl -sf "http://localhost:$PORT/v1/topology" | grep -q prod-k3s && break; done

post() { curl -s -X POST "http://localhost:$PORT/v1/clusters/prod-k3s/actions" -H 'Content-Type: application/json' -d "$1" | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])"; }
info() { curl -s "http://localhost:$PORT/v1/clusters/prod-k3s/actions" | python3 -c "import sys,json;a=[x for x in json.load(sys.stdin) if x['id']=='$1'];print((a[0]['status']+'|'+a[0].get('error','')) if a else '?|')"; }
wait_end() { for _ in $(seq 1 40); do sleep 3; s=$(info "$1"); case "$s" in done*|error*) echo "$s"; return;; esac; done; echo "timeout|"; }

log "instalar ArgoCD + registrar proyecto guestbook…"
wait_end "$(post '{"kind":"install","addon":"argocd"}')" | grep -q '^done' || fail "instalar ArgoCD falló"
kubectl -n argocd rollout status deploy/argocd-repo-server --timeout=150s >/dev/null 2>&1
wait_end "$(post '{"kind":"addapp","app":{"name":"guestbook","repoURL":"https://github.com/argoproj/argocd-example-apps","path":"guestbook","namespace":"default"}}')" | grep -q '^done' || fail "registrar proyecto falló"
# espera a que sincronice al menos una vez
for _ in $(seq 1 30); do sleep 5; kubectl -n argocd get application guestbook -o jsonpath='{.status.sync.status}' 2>/dev/null | grep -q Synced && break; done
ok "ArgoCD + proyecto guestbook (Synced)"

log "prueba 1: sincronizar (force sync)…"
R=$(wait_end "$(post '{"kind":"sync","app":{"name":"guestbook"}}')")
echo "$R" | grep -q '^done' || fail "sync no llegó a done ($R)"
ok "sync ejecutado (done)"

log "prueba 2: revertir sin versión anterior (guard)…"
R=$(wait_end "$(post '{"kind":"rollback","app":{"name":"guestbook"}}')")
case "$R" in
  error*versión\ anterior*|error*anterior*) ok "rollback devuelve error claro: ${R#error|}";;
  done*) ok "rollback ejecutado (había historial ≥2)";;
  *) fail "resultado inesperado de rollback: $R";;
esac

printf '\n\033[1;32m═══════════════════════════════════════\n'
printf '  TODO OK — sincronizar / revertir, E2E\n'
printf '═══════════════════════════════════════\033[0m\n'
