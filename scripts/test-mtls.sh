#!/usr/bin/env bash
# Prueba de extremo a extremo del mTLS entre agente y control plane.
# Genera una PKI, arranca el control plane sobre HTTPS exigiendo certificado de
# cliente, y comprueba tres cosas:
#   1) un cliente SIN certificado es rechazado (handshake),
#   2) un agente CON certificado válido se registra,
#   3) un certificado de OTRA CA (impostor) es rechazado.
# No necesita clúster (usa el colector sample). Limpia todo al terminar.
#
# Requisitos: go 1.22+, curl.
#   ./scripts/test-mtls.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"
CERTS="$WORKDIR/certs"
ROGUE="$WORKDIR/rogue"
CP_PID=""; AGENT_PID=""

log()  { printf '\033[1;36m▶ %s\033[0m\n' "$*"; }
ok()   { printf '\033[1;32m✓ %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

cleanup() {
  [ -n "$AGENT_PID" ] && kill "$AGENT_PID" 2>/dev/null || true
  [ -n "$CP_PID" ]    && kill "$CP_PID"    2>/dev/null || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

log "compilando binarios…"
cd "$ROOT"
go build -o "$WORKDIR/atlas-certs" ./cmd/atlas-certs
go build -o "$WORKDIR/controlplane" ./cmd/controlplane
go build -o "$WORKDIR/agent" ./cmd/agent

log "generando PKI (CA + servidor + clientes)…"
"$WORKDIR/atlas-certs" bundle --out "$CERTS" --hosts localhost,127.0.0.1 >/dev/null
"$WORKDIR/atlas-certs" client --out "$CERTS" --name prod-eks >/dev/null
"$WORKDIR/atlas-certs" bundle --out "$ROGUE" --hosts localhost >/dev/null # CA impostora
ok "PKI lista"

PORT=39443; while ss -ltn 2>/dev/null | grep -q ":$PORT "; do PORT=$((PORT+1)); done
log "arrancando control plane con mTLS en :$PORT…"
ATLAS_ADDR=":$PORT" "$WORKDIR/controlplane" \
  --tls-cert "$CERTS/server.crt" --tls-key "$CERTS/server.key" --tls-client-ca "$CERTS/ca.crt" \
  >"$WORKDIR/cp.log" 2>&1 &
CP_PID=$!
sleep 1.5

log "prueba 1: cliente SIN certificado…"
if curl -sf --cacert "$CERTS/ca.crt" "https://localhost:$PORT/v1/topology" >/dev/null 2>&1; then
  fail "un cliente sin certificado NO debería ser aceptado"
fi
ok "rechazado (handshake), como debe"

log "prueba 2: cliente con certificado de OTRA CA…"
if curl -sf --cacert "$CERTS/ca.crt" --cert "$ROGUE/agent.crt" --key "$ROGUE/agent.key" \
     "https://localhost:$PORT/v1/topology" >/dev/null 2>&1; then
  fail "un certificado de CA impostora NO debería ser aceptado"
fi
ok "certificado impostor rechazado — la CA verifica de verdad"

log "prueba 3: agente con certificado VÁLIDO se registra…"
"$WORKDIR/agent" --control-plane "https://localhost:$PORT" --name "prod eks" --provider aws --cluster-id prod-eks \
  --tls-cert "$CERTS/prod-eks.crt" --tls-key "$CERTS/prod-eks.key" --tls-ca "$CERTS/ca.crt" \
  >"$WORKDIR/agent.log" 2>&1 &
AGENT_PID=$!
ONLINE=""
for _ in $(seq 1 10); do
  sleep 1.5
  ONLINE=$(curl -s --cacert "$CERTS/ca.crt" --cert "$CERTS/agent.crt" --key "$CERTS/agent.key" \
    "https://localhost:$PORT/v1/topology" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d['clusters'][0]['online'] if d['clusters'] else False)" 2>/dev/null || echo False)
  [ "$ONLINE" = "True" ] && break
done
[ "$ONLINE" = "True" ] || fail "el agente con certificado válido no se registró"
ok "agente registrado sobre HTTPS con mTLS y online"

printf '\n\033[1;32m═══════════════════════════════════════\n'
printf '  TODO OK — mTLS verificado E2E\n'
printf '═══════════════════════════════════════\033[0m\n'
