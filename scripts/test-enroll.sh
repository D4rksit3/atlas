#!/usr/bin/env bash
# E2E de la vinculación por token: el control plane (con la CA montada) emite
# un token de un solo uso; canjearlo devuelve un manifiesto autocontenido con
# un certificado mTLS RECIÉN emitido y firmado por la CA. No necesita clúster.
#
#   1. Sin CA montada, POST /v1/enroll explica qué falta (400)
#   2. Con CA: crear token exige rol operator (401 sin sesión)
#   3. El token canjeado devuelve un manifiesto válido (Secret+Deployment+URL)
#   4. El certificado incluido VERIFICA contra la CA (openssl) y es de vida corta
#   5. El token es de UN SOLO USO (el segundo canje da 404)
#   6. Un token inventado da 404
#   7. Todo queda auditado (creación y canje)
set -euo pipefail
cd "$(dirname "$0")/.."

PORT=18095
PASS="secreta-enroll-1"
DIR=$(mktemp -d)
LOG="$DIR/cp.log"
fail() { echo "FALLO: $*" >&2; tail -20 "$LOG" 2>/dev/null; exit 1; }
cleanup() { kill $CP_PID 2>/dev/null || true; rm -rf "$DIR"; }
trap cleanup EXIT

echo ">> compilando y generando la CA de prueba"
GOTOOLCHAIN=local go build -o bin/controlplane ./cmd/controlplane
GOTOOLCHAIN=local go run ./cmd/atlas-certs init --out "$DIR/pki" >/dev/null

echo ">> 1) sin CA montada: el error explica qué falta"
ATLAS_ADMIN_PASSWORD="$PASS" ./bin/controlplane --addr ":$PORT" >"$LOG" 2>&1 &
CP_PID=$!
for i in $(seq 1 50); do curl -sf "http://localhost:$PORT/healthz" >/dev/null 2>&1 && break; sleep 0.2; done
TOKEN=$(curl -sf -X POST "http://localhost:$PORT/v1/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$PASS\"}" | grep -o '"token":"[^"]*"' | cut -d'"' -f4)
resp=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"lab"}' "http://localhost:$PORT/v1/enroll")
echo "$resp" | grep -q "monta la CA" || fail "sin CA debería explicar qué falta: $resp"
kill $CP_PID; wait $CP_PID 2>/dev/null || true

echo ">> arrancando con la CA montada"
ATLAS_ADMIN_PASSWORD="$PASS" ./bin/controlplane --addr ":$PORT" \
  --ca-cert "$DIR/pki/ca.crt" --ca-key "$DIR/pki/ca.key" \
  --agent-public-url "https://atlas-cp.ejemplo.com:8443" \
  --agent-image "harbor.ejemplo.com/atlas/atlas-agent:v1" >"$LOG" 2>&1 &
CP_PID=$!
for i in $(seq 1 50); do curl -sf "http://localhost:$PORT/healthz" >/dev/null 2>&1 && break; sleep 0.2; done
TOKEN=$(curl -sf -X POST "http://localhost:$PORT/v1/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$PASS\"}" | grep -o '"token":"[^"]*"' | cut -d'"' -f4)

echo ">> 2) crear token exige sesión de operator"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d '{"name":"pirata"}' "http://localhost:$PORT/v1/enroll")
[ "$code" = "401" ] || fail "sin sesión esperaba 401, obtuve $code"
ET=$(curl -sf -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"planta lima","provider":"onprem"}' "http://localhost:$PORT/v1/enroll" \
  | grep -o '"token":"[^"]*"' | cut -d'"' -f4)
[ -n "$ET" ] || fail "no llegó el token de vinculación"

echo ">> 3) canjear el token devuelve el manifiesto autocontenido"
curl -sf "http://localhost:$PORT/v1/enroll/$ET" > "$DIR/manifest.yaml"
for pieza in "kind: Namespace" "kind: Secret" "kind: Deployment" \
  "https://atlas-cp.ejemplo.com:8443" "harbor.ejemplo.com/atlas/atlas-agent:v1" \
  "--cluster-id=planta-lima" "ATLAS_TLS_CERT"; do
  grep -q -- "$pieza" "$DIR/manifest.yaml" || fail "el manifiesto no contiene: $pieza"
done

echo ">> 4) el certificado emitido verifica contra la CA y es de vida corta"
awk '/tls.crt: \|/{f=1;next} f&&/tls.key:/{exit} f{sub(/^    /,"");print}' \
  "$DIR/manifest.yaml" > "$DIR/agent.crt"
openssl verify -CAfile "$DIR/pki/ca.crt" "$DIR/agent.crt" >/dev/null \
  || fail "el cert emitido no verifica contra la CA"
end=$(openssl x509 -in "$DIR/agent.crt" -noout -enddate | cut -d= -f2)
end_epoch=$(date -d "$end" +%s); max=$(date -d "+91 days" +%s)
[ "$end_epoch" -le "$max" ] || fail "el cert dura más de 90 días ($end)"
openssl x509 -in "$DIR/agent.crt" -noout -subject | grep -q "agent-planta-lima" \
  || fail "el CN no es agent-planta-lima"

echo ">> 5) un solo uso: el segundo canje falla"
code=$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:$PORT/v1/enroll/$ET")
[ "$code" = "404" ] || fail "reuso del token: esperaba 404, obtuve $code"

echo ">> 6) token inventado: 404"
code=$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:$PORT/v1/enroll/no-existe-123")
[ "$code" = "404" ] || fail "token falso: esperaba 404, obtuve $code"

echo ">> 7) auditoría de creación y canje"
audit=$(curl -sf -H "Authorization: Bearer $TOKEN" "http://localhost:$PORT/v1/audit")
echo "$audit" | grep -q 'token de vinculación para' || fail "no se auditó la creación"
echo "$audit" | grep -q 'canjeado' || fail "no se auditó el canje"

echo
echo "OK: vinculación por token verificada (un solo uso, cert firmado por la CA, auditada)"
