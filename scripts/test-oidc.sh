#!/usr/bin/env bash
# Prueba de extremo a extremo de la auth de la GUI (OIDC + RBAC). Levanta un IdP
# OIDC de prueba (hack/mock-oidc, firma RS256 real) y un control plane con auth
# activa, y comprueba:
#   1) sin token            -> 401 en /v1/topology
#   2) token de 'viewer'    -> 200 al leer, 403 al intentar operar
#   3) token de 'operator'  -> 202 al encolar una acción
# No necesita clúster de K8s (el actuador no ejecuta; solo probamos autorización).
#
# Requisitos: go 1.22+, curl.
#   ./scripts/test-oidc.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"
MOCK_PID=""; CP_PID=""; AGENT_PID=""

log()  { printf '\033[1;36m▶ %s\033[0m\n' "$*"; }
ok()   { printf '\033[1;32m✓ %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

cleanup() {
  for p in "$AGENT_PID" "$CP_PID" "$MOCK_PID"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

command -v curl >/dev/null || fail "falta curl"

log "compilando (control plane, agente, mock-oidc)…"
cd "$ROOT"
go build -o "$WORKDIR/controlplane" ./cmd/controlplane
go build -o "$WORKDIR/agent" ./cmd/agent
go build -o "$WORKDIR/mock-oidc" ./hack/mock-oidc

freeport() { local p=$1; while ss -ltn 2>/dev/null | grep -q ":$p "; do p=$((p+1)); done; echo "$p"; }
MP=$(freeport 39900); ISS="http://localhost:$MP"
CP=$(freeport 39600)

log "arrancando IdP OIDC de prueba en $ISS…"
"$WORKDIR/mock-oidc" -addr ":$MP" -client atlas-gui >"$WORKDIR/mock.log" 2>&1 &
MOCK_PID=$!
for _ in $(seq 1 10); do sleep 0.5; curl -sf "$ISS/jwks" >/dev/null 2>&1 && break; done

log "arrancando control plane con OIDC (operador = ops@atlas.dev)…"
ATLAS_ADDR=":$CP" "$WORKDIR/controlplane" \
  --oidc-issuer "$ISS" --oidc-client-id atlas-gui --rbac-operators "ops@atlas.dev" \
  >"$WORKDIR/cp.log" 2>&1 &
CP_PID=$!
sleep 1.5
grep -q "OIDC activa" "$WORKDIR/cp.log" || { cat "$WORKDIR/cp.log"; fail "el control plane no activó OIDC"; }

log "registrando un clúster (endpoint de agente, sin OIDC)…"
"$WORKDIR/agent" --control-plane "http://localhost:$CP" --name demo --provider onprem --cluster-id demo \
  >"$WORKDIR/agent.log" 2>&1 &
AGENT_PID=$!
sleep 3

VIEWER=$(curl -s "$ISS/mint?email=alguien@atlas.dev")
OPERATOR=$(curl -s "$ISS/mint?email=ops@atlas.dev")
ACTION='{"kind":"scale","namespace":"default","workload":"web","workloadKind":"Deployment","replicas":3}'
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

log "prueba 1: sin token → /v1/topology…"
C=$(code "http://localhost:$CP/v1/topology")
[ "$C" = "401" ] || fail "esperaba 401 sin token, obtuve $C"
ok "401 sin token"

log "prueba 2a: viewer lee topología…"
C=$(code -H "Authorization: Bearer $VIEWER" "http://localhost:$CP/v1/topology")
[ "$C" = "200" ] || fail "esperaba 200 para viewer, obtuve $C"
ok "200 viewer puede leer"

log "prueba 2b: viewer intenta OPERAR (debe fallar)…"
C=$(code -X POST -H "Authorization: Bearer $VIEWER" -H 'Content-Type: application/json' -d "$ACTION" "http://localhost:$CP/v1/clusters/demo/actions")
[ "$C" = "403" ] || fail "esperaba 403 para viewer operando, obtuve $C"
ok "403 viewer NO puede operar (RBAC)"

log "prueba 3: operator opera…"
C=$(code -X POST -H "Authorization: Bearer $OPERATOR" -H 'Content-Type: application/json' -d "$ACTION" "http://localhost:$CP/v1/clusters/demo/actions")
[ "$C" = "202" ] || fail "esperaba 202 para operator, obtuve $C"
ok "202 operator puede operar"

printf '\n\033[1;32m═══════════════════════════════════════\n'
printf '  TODO OK — OIDC + RBAC verificado E2E\n'
printf '═══════════════════════════════════════\033[0m\n'
