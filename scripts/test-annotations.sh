#!/usr/bin/env bash
# Prueba de extremo a extremo de EDITAR EL MAPA (anotaciones/metadatos). Con OIDC
# activo comprueba:
#   1) un 'viewer' NO puede editar (403),
#   2) un 'operator' renombra/colorea una entidad (200) y GET la devuelve,
#   3) la edición queda en la auditoría (map.edited) atribuida al usuario.
# No necesita clúster de K8s.
#
# Requisitos: go 1.22+, curl.
#   ./scripts/test-annotations.sh
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

log "IdP de prueba + control plane OIDC (operator = ops@atlas.dev)…"
"$WORKDIR/mock-oidc" -addr ":$MP" -client atlas-gui >"$WORKDIR/mock.log" 2>&1 &
MOCK_PID=$!
for _ in $(seq 1 10); do sleep 0.5; curl -sf "$ISS/jwks" >/dev/null 2>&1 && break; done
ATLAS_ADDR=":$CP" "$WORKDIR/controlplane" --oidc-issuer "$ISS" --oidc-client-id atlas-gui --rbac-operators "ops@atlas.dev" >"$WORKDIR/cp.log" 2>&1 &
CP_PID=$!
sleep 1.5
"$WORKDIR/agent" --control-plane "http://localhost:$CP" --name demo --provider onprem --cluster-id demo >"$WORKDIR/agent.log" 2>&1 &
AGENT_PID=$!
sleep 3

VIEWER=$(curl -s "$ISS/mint?email=alguien@atlas.dev")
OPERATOR=$(curl -s "$ISS/mint?email=ops@atlas.dev")
KEY="demo/default/web"
BODY='{"displayName":"Frontend Web","color":"#2D74DA","note":"dueño: equipo plataforma"}'
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

log "prueba 1: viewer intenta editar (debe fallar)…"
C=$(code -X PUT -H "Authorization: Bearer $VIEWER" -H 'Content-Type: application/json' -d "$BODY" "http://localhost:$CP/v1/annotations/$KEY")
[ "$C" = "403" ] || fail "esperaba 403 para viewer editando, obtuve $C"
ok "403 viewer NO puede editar el mapa"

log "prueba 2: operator renombra/colorea…"
C=$(code -X PUT -H "Authorization: Bearer $OPERATOR" -H 'Content-Type: application/json' -d "$BODY" "http://localhost:$CP/v1/annotations/$KEY")
[ "$C" = "200" ] || fail "esperaba 200 para operator, obtuve $C"
NAME=$(curl -s -H "Authorization: Bearer $VIEWER" "http://localhost:$CP/v1/annotations" | python3 -c "import sys,json;print(json.load(sys.stdin).get('$KEY',{}).get('displayName',''))")
[ "$NAME" = "Frontend Web" ] || fail "la anotación no se guardó (displayName='$NAME')"
ok "operator editó el mapa y GET lo devuelve: '$NAME'"

log "prueba 3: la edición quedó en la auditoría…"
AUDIT=$(curl -s -H "Authorization: Bearer $VIEWER" "http://localhost:$CP/v1/audit")
echo "$AUDIT" | python3 -c "
import sys,json
e=[x for x in json.load(sys.stdin) if x['event']=='map.edited']
assert e, 'no hay entrada map.edited'
assert e[0]['actor']=='ops@atlas.dev', 'actor incorrecto: '+e[0]['actor']
print('  auditoría:', e[0]['actor'], '·', e[0]['summary'])
" || fail "la auditoría no registró la edición"
ok "la edición del mapa quedó auditada y atribuida"

printf '\n\033[1;32m═══════════════════════════════════════\n'
printf '  TODO OK — editar el mapa (RBAC + auditoría) E2E\n'
printf '═══════════════════════════════════════\033[0m\n'
