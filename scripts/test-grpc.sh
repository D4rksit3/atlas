#!/usr/bin/env bash
# Prueba E2E del transporte gRPC bidireccional (canal de órdenes al instante).
# La clave del test: el intervalo de snapshots se sube A PROPÓSITO a 30s; si la
# acción encolada desde la API llega al agente en <5s NO pudo ser el polling —
# la empujó el stream. Tres fases, sin clúster (colector sample):
#   1) h2c (sin TLS): agente conecta por gRPC, REST convive en el mismo puerto,
#      una acción hace el viaje completo (push -> ejecuta -> resultado) en <5s.
#   2) reconexión: el control plane muere y renace (memoria = amnesia); el
#      agente reconecta, se re-registra y vuelve online solo.
#   3) mTLS: el MISMO puerto :N sirve gRPC + REST sobre TLS con certificado de
#      cliente (ALPN h2), y el viaje instantáneo funciona igual.
# Limpia todo al terminar.
#
# Requisitos: go 1.22+, curl, python3.
#   ./scripts/test-grpc.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"
CERTS="$WORKDIR/certs"
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

PORT=38080; while ss -ltn 2>/dev/null | grep -q ":$PORT "; do PORT=$((PORT+1)); done

# wait_online CURL_ARGS... — espera a que el clúster esté online en /v1/topology.
wait_online() {
  local online=""
  for _ in $(seq 1 20); do
    online=$(curl -s "$@" "$BASE/v1/topology" | python3 -c \
      "import sys,json;d=json.load(sys.stdin);print(d['clusters'][0]['online'] if d['clusters'] else False)" 2>/dev/null || echo False)
    [ "$online" = "True" ] && return 0
    sleep 1
  done
  return 1
}

# push_roundtrip CURL_ARGS... — encola una acción y mide cuánto tarda el viaje
# completo (push por el stream -> el agente la ejecuta -> el resultado vuelve y
# queda registrado). Falla si tarda >=5s: con snapshots cada 30s, solo el
# empuje del stream puede llegar tan rápido.
push_roundtrip() {
  local t0 t1 status elapsed_ms
  t0=$(date +%s%3N)
  curl -sf "$@" -X POST "$BASE/v1/clusters/demo/actions" \
    -H 'Content-Type: application/json' \
    -d '{"kind":"scale","namespace":"default","workload":"web","workloadKind":"Deployment","replicas":2}' >/dev/null \
    || fail "no se pudo encolar la acción"
  for _ in $(seq 1 50); do
    status=$(curl -s "$@" "$BASE/v1/clusters/demo/actions" | python3 -c \
      "import sys,json;a=json.load(sys.stdin);print(a[0]['status'] if a else 'none')" 2>/dev/null || echo none)
    case "$status" in done|error) break ;; esac
    sleep 0.2
  done
  t1=$(date +%s%3N)
  elapsed_ms=$((t1 - t0))
  # El colector sample no tiene actuador: el resultado esperado es 'error' con
  # el rechazo del agente — lo que importa es que el VIAJE COMPLETO ocurrió.
  [ "$status" = "error" ] || fail "la acción no completó el viaje (status=$status)"
  [ "$elapsed_ms" -lt 5000 ] || fail "tardó ${elapsed_ms}ms: eso es polling, no push (snapshots cada 30s)"
  ok "viaje completo en ${elapsed_ms}ms con snapshots cada 30s — la orden viajó por el stream"
}

# ---------- Fase 1: gRPC en claro (h2c) + REST en el mismo puerto ----------
log "fase 1: control plane en :$PORT (sin TLS, snapshots cada 30s)…"
BASE="http://localhost:$PORT"
ATLAS_ADDR=":$PORT" "$WORKDIR/controlplane" --heartbeat 30 >"$WORKDIR/cp.log" 2>&1 &
CP_PID=$!
sleep 1

"$WORKDIR/agent" --control-plane "$BASE" --name demo --cluster-id demo \
  --transport grpc >"$WORKDIR/agent.log" 2>&1 &
AGENT_PID=$!

wait_online || fail "el agente no llegó online por gRPC (mira $WORKDIR/agent.log)"
grep -q "conectado por gRPC" "$WORKDIR/agent.log" || fail "el agente no conectó por gRPC"
ok "agente online por stream gRPC; REST responde en el mismo puerto"

streams=$(curl -s "$BASE/metrics" | awk '/^atlas_agent_streams /{print $2}')
[ "$streams" = "1" ] || fail "atlas_agent_streams=$streams (esperaba 1)"
ok "métrica atlas_agent_streams=1 (stream contabilizado)"

log "fase 1: empuje instantáneo de una orden…"
push_roundtrip

# ---------- Fase 2: reconexión tras caída del control plane ----------
log "fase 2: mato el control plane y lo resucito (memoria = amnesia)…"
kill "$CP_PID"; wait "$CP_PID" 2>/dev/null || true
sleep 2
ATLAS_ADDR=":$PORT" "$WORKDIR/controlplane" --heartbeat 30 >"$WORKDIR/cp2.log" 2>&1 &
CP_PID=$!
wait_online || fail "el agente no se re-registró tras la caída"
ok "el agente reconectó y se re-registró solo (backoff + re-hello)"

kill "$AGENT_PID" 2>/dev/null || true; AGENT_PID=""
kill "$CP_PID" 2>/dev/null || true; CP_PID=""

# ---------- Fase 3: gRPC sobre mTLS, mismo puerto que la API ----------
log "fase 3: PKI + control plane con mTLS en :$PORT…"
"$WORKDIR/atlas-certs" bundle --out "$CERTS" --hosts localhost,127.0.0.1 >/dev/null
"$WORKDIR/atlas-certs" client --out "$CERTS" --name demo >/dev/null
BASE="https://localhost:$PORT"
CURL_TLS=(--cacert "$CERTS/ca.crt" --cert "$CERTS/agent.crt" --key "$CERTS/agent.key")
ATLAS_ADDR=":$PORT" "$WORKDIR/controlplane" --heartbeat 30 \
  --tls-cert "$CERTS/server.crt" --tls-key "$CERTS/server.key" --tls-client-ca "$CERTS/ca.crt" \
  >"$WORKDIR/cp3.log" 2>&1 &
CP_PID=$!
sleep 1

"$WORKDIR/agent" --control-plane "$BASE" --name demo --cluster-id demo --transport grpc \
  --tls-cert "$CERTS/demo.crt" --tls-key "$CERTS/demo.key" --tls-ca "$CERTS/ca.crt" \
  >"$WORKDIR/agent3.log" 2>&1 &
AGENT_PID=$!

wait_online "${CURL_TLS[@]}" || fail "el agente no llegó online por gRPC+mTLS (mira $WORKDIR/agent3.log)"
grep -q "conectado por gRPC" "$WORKDIR/agent3.log" || fail "el agente no conectó por gRPC (mTLS)"
ok "stream gRPC autenticado por mTLS en el mismo puerto que la API"

log "fase 3: empuje instantáneo sobre mTLS…"
push_roundtrip "${CURL_TLS[@]}"

printf '\n\033[1;32m═══════════════════════════════════════════════════════\n'
printf '  TODO OK — gRPC bidireccional verificado E2E\n'
printf '  (push instantáneo, reconexión y mTLS en un solo puerto)\n'
printf '═══════════════════════════════════════════════════════\033[0m\n'
