#!/usr/bin/env bash
# Prueba E2E de VALORES EDITABLES al instalar un complemento Helm. Instala
# kube-prometheus-stack con valores personalizados (contraseña de Grafana y
# retención de Prometheus) y verifica que se aplicaron en los paths vetados.
# Limpia todo al terminar.
#
# Requisitos: docker, k3d, kubectl, go 1.22+, curl, internet.
#   ./scripts/test-values.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER=atlas-values
WORKDIR="$(mktemp -d)"
CP_PID=""; AGENT_PID=""
PW="Atl4sSecret!"; RET="5d"

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

log "instalar Prometheus + Grafana con valores personalizados…"
ID=$(curl -s -X POST "http://localhost:$PORT/v1/clusters/prod-k3s/actions" -H 'Content-Type: application/json' \
  -d "{\"kind\":\"install\",\"addon\":\"kube-prometheus-stack\",\"values\":{\"grafanaPassword\":\"$PW\",\"retention\":\"$RET\"}}" \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
for _ in $(seq 1 60); do
  sleep 6
  s=$(curl -s "http://localhost:$PORT/v1/clusters/prod-k3s/actions" | python3 -c "import sys,json;a=[x for x in json.load(sys.stdin) if x['id']=='$ID'];print(a[0]['status'] if a else '?')")
  [ "$s" = "done" ] && break
  [ "$s" = "error" ] && { curl -s "http://localhost:$PORT/v1/clusters/prod-k3s/actions" | python3 -c "import sys,json;print([a.get('error') for a in json.load(sys.stdin) if a['id']=='$ID'])"; fail "la instalación falló"; }
done
[ "$s" = "done" ] || fail "la instalación no llegó a done"
ok "instalado (con valores)"

log "verificando que los valores se aplicaron…"
GOT_PW=$(kubectl -n monitoring get secret kube-prometheus-stack-grafana -o jsonpath='{.data.admin-password}' 2>/dev/null | base64 -d)
[ "$GOT_PW" = "$PW" ] || fail "la contraseña de Grafana no se aplicó (got '$GOT_PW')"
ok "contraseña de Grafana aplicada"
GOT_RET=$(kubectl -n monitoring get prometheus -o jsonpath='{.items[0].spec.retention}' 2>/dev/null)
[ "$GOT_RET" = "$RET" ] || fail "la retención de Prometheus no se aplicó (got '$GOT_RET')"
ok "retención de Prometheus aplicada ($GOT_RET)"

printf '\n\033[1;32m═══════════════════════════════════════════\n'
printf '  TODO OK — valores editables (Helm), verificado E2E\n'
printf '═══════════════════════════════════════════\033[0m\n'
