#!/usr/bin/env bash
# Prueba E2E del soporte de HELM: instalar un complemento basado en Helm (Falco,
# seguridad) desde la GUI. El agente usa el SDK de Helm compilado en él (sin el
# binario helm). Comprueba:
#   1) la acción install llega a 'done',
#   2) se crea un RELEASE de Helm de verdad (secret sh.helm.release...),
#   3) se despliega Falco (DaemonSet) y Atlas lo detecta.
# (Los pods de Falco pueden no arrancar en k3d por el eBPF anidado; verificamos la
# instalación, no que Falco quede Ready.) Limpia todo al terminar.
#
# Requisitos: docker, k3d, kubectl, go 1.22+, curl, internet.
#   ./scripts/test-helm.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER=atlas-helm
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

log "instalar Falco (Helm) desde la GUI…"
ID=$(curl -s -X POST "http://localhost:$PORT/v1/clusters/prod-k3s/actions" -H 'Content-Type: application/json' -d '{"kind":"install","addon":"falco"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
for _ in $(seq 1 50); do
  sleep 4
  s=$(curl -s "http://localhost:$PORT/v1/clusters/prod-k3s/actions" | python3 -c "import sys,json;a=[x for x in json.load(sys.stdin) if x['id']=='$ID'];print(a[0]['status'] if a else '?')")
  [ "$s" = "done" ] && break
  [ "$s" = "error" ] && { curl -s "http://localhost:$PORT/v1/clusters/prod-k3s/actions" | python3 -c "import sys,json;print([a.get('error') for a in json.load(sys.stdin) if a['id']=='$ID'])"; fail "instalar Falco falló"; }
done
[ "$s" = "done" ] || fail "la instalación de Falco no llegó a done"
ok "acción install (Helm) en 'done'"

log "verificando el release de Helm y Falco…"
kubectl -n falco get secret -l owner=helm 2>/dev/null | grep -q "sh.helm.release.v1.falco" || fail "no se creó el release de Helm"
ok "release de Helm creado (sh.helm.release.v1.falco.v1)"
kubectl -n falco get daemonset falco >/dev/null 2>&1 || fail "no existe el DaemonSet de Falco"
ok "Falco desplegado (DaemonSet)"

log "Atlas lo detecta…"
for _ in $(seq 1 10); do
  sleep 3
  SEEN=$(curl -s "http://localhost:$PORT/v1/topology" | python3 -c "import sys,json;c=json.load(sys.stdin)['clusters'][0];print(any(w['namespace']=='falco' and w['name']=='falco' for w in c['snapshot']['workloads']))" 2>/dev/null)
  [ "$SEEN" = "True" ] && break
done
[ "$SEEN" = "True" ] || fail "Atlas no ve Falco"
ok "Atlas ve Falco (DaemonSet) — detección de 'instalado'"

printf '\n\033[1;32m═══════════════════════════════════════════\n'
printf '  TODO OK — soporte de Helm (Falco), verificado E2E\n'
printf '═══════════════════════════════════════════\033[0m\n'
