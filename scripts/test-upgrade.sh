#!/usr/bin/env bash
# Prueba E2E del UPGRADE de un complemento Helm: instalar kube-prometheus-stack
# con retention=10d y luego "reinstalar" (upgrade) con retention=30d; verifica que
# el valor cambió (helm upgrade con ReuseValues). Limpia todo al terminar.
#
# Requisitos: docker, k3d, kubectl, go 1.22+, curl, internet.
#   ./scripts/test-upgrade.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER=atlas-upgrade
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

act() { curl -s -X POST "http://localhost:$PORT/v1/clusters/prod-k3s/actions" -H 'Content-Type: application/json' -d "$1" | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])"; }
waitd() { for _ in $(seq 1 60); do sleep 5; s=$(curl -s "http://localhost:$PORT/v1/clusters/prod-k3s/actions" | python3 -c "import sys,json;a=[x for x in json.load(sys.stdin) if x['id']=='$1'];print(a[0]['status'] if a else '?')"); case "$s" in done|error) echo "$s"; return;; esac; done; echo timeout; }
retention() { kubectl -n monitoring get prometheus -o jsonpath='{.items[0].spec.retention}' 2>/dev/null; }

log "instalar con retention=10d…"
[ "$(waitd "$(act '{"kind":"install","addon":"kube-prometheus-stack","values":{"retention":"10d"}}')")" = done ] || fail "install falló"
for _ in $(seq 1 12); do sleep 3; [ "$(retention)" = "10d" ] && break; done
[ "$(retention)" = "10d" ] || fail "retention inicial != 10d ($(retention))"
ok "instalado con retention=10d"

log "upgrade con retention=30d…"
[ "$(waitd "$(act '{"kind":"install","addon":"kube-prometheus-stack","values":{"retention":"30d"}}')")" = done ] || fail "upgrade falló"
for _ in $(seq 1 12); do sleep 3; [ "$(retention)" = "30d" ] && break; done
[ "$(retention)" = "30d" ] || fail "el upgrade no cambió la retención (sigue $(retention))"
ok "upgrade aplicado: retention=30d"

printf '\n\033[1;32m═══════════════════════════════════════\n'
printf '  TODO OK — upgrade de complemento Helm, E2E\n'
printf '═══════════════════════════════════════\033[0m\n'
