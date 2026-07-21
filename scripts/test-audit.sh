#!/usr/bin/env bash
# Prueba de extremo a extremo del registro de AUDITORÍA con atribución de usuario.
# Con OIDC activo, un 'operator' encola una acción; comprobamos que la auditoría
# registra QUIÉN la pidió (action.requested) y su resultado (action.executed).
# Usa el agente sample (sin actuador) → el 'executed' sale con outcome=error, lo
# que igualmente demuestra el ciclo completo y la atribución. No necesita K8s.
#
# Requisitos: go 1.22+, curl.
#   ./scripts/test-audit.sh
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

log "compilando…"
cd "$ROOT"
go build -o "$WORKDIR/controlplane" ./cmd/controlplane
go build -o "$WORKDIR/agent" ./cmd/agent
go build -o "$WORKDIR/mock-oidc" ./hack/mock-oidc

freeport() { local p=$1; while ss -ltn 2>/dev/null | grep -q ":$p "; do p=$((p+1)); done; echo "$p"; }
MP=$(freeport 39900); ISS="http://localhost:$MP"; CP=$(freeport 39600)

log "IdP de prueba + control plane con OIDC (operator = ops@atlas.dev)…"
"$WORKDIR/mock-oidc" -addr ":$MP" -client atlas-gui >"$WORKDIR/mock.log" 2>&1 &
MOCK_PID=$!
for _ in $(seq 1 10); do sleep 0.5; curl -sf "$ISS/jwks" >/dev/null 2>&1 && break; done
ATLAS_ADDR=":$CP" "$WORKDIR/controlplane" --oidc-issuer "$ISS" --oidc-client-id atlas-gui --rbac-operators "ops@atlas.dev" >"$WORKDIR/cp.log" 2>&1 &
CP_PID=$!
sleep 1.5
"$WORKDIR/agent" --control-plane "http://localhost:$CP" --name demo --provider onprem --cluster-id demo >"$WORKDIR/agent.log" 2>&1 &
AGENT_PID=$!
sleep 3

OPERATOR=$(curl -s "$ISS/mint?email=ops@atlas.dev")
audit() { curl -s -H "Authorization: Bearer $OPERATOR" "http://localhost:$CP/v1/audit"; }

log "el operador encola una acción de escalado…"
curl -s -o /dev/null -X POST -H "Authorization: Bearer $OPERATOR" -H 'Content-Type: application/json' \
  -d '{"kind":"scale","namespace":"default","workload":"web","workloadKind":"Deployment","replicas":4}' \
  "http://localhost:$CP/v1/clusters/demo/actions"

log "comprobando la auditoría…"
# 1) entrada 'requested' atribuida a ops@atlas.dev
for _ in $(seq 1 10); do
  sleep 1
  audit | grep -q '"event":"action.requested"' && break
done
REQ_ACTOR=$(audit | python3 -c "import sys,json;e=[x for x in json.load(sys.stdin) if x['event']=='action.requested'];print(e[0]['actor'] if e else '')")
[ "$REQ_ACTOR" = "ops@atlas.dev" ] || fail "esperaba actor ops@atlas.dev en 'requested', vi '$REQ_ACTOR'"
ok "auditoría registra QUIÉN pidió la acción: $REQ_ACTOR"

# 2) entrada 'executed' (el agente sample no puede ejecutar -> outcome error)
for _ in $(seq 1 15); do
  sleep 1.5
  audit | grep -q '"event":"action.executed"' && break
done
EXEC=$(audit | python3 -c "import sys,json;e=[x for x in json.load(sys.stdin) if x['event']=='action.executed'];print(e[0]['outcome'] if e else '')")
[ -n "$EXEC" ] || fail "no apareció la entrada 'executed'"
ok "auditoría registra el RESULTADO de la ejecución (outcome=$EXEC)"

echo ""; echo "  Registro de auditoría:"
audit | python3 -c "
import sys,json
for e in reversed(json.load(sys.stdin)):
    print(f\"    [{e['outcome']:7s}] {e['actor']} · {e['event'].split('.')[1]} · {e['summary']}\")"

printf '\n\033[1;32m═══════════════════════════════════════\n'
printf '  TODO OK — auditoría con atribución, E2E\n'
printf '═══════════════════════════════════════\033[0m\n'
