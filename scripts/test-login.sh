#!/usr/bin/env bash
# E2E del login local integrado: la API queda CERRADA sin sesión y se abre con
# usuario/contraseña. No necesita clúster: levanta el control plane en local.
#
#   1. Sin token          -> 401 en /v1/topology
#   2. Contraseña mala    -> 401 en /v1/login
#   3. Contraseña buena   -> token de sesión
#   4. Con token          -> 200 en /v1/topology (y en /v1/audit)
#   5. Token manipulado   -> 401 (la firma HMAC no cuadra)
#   6. La auditoría refleja el intento fallido y el correcto
#   7. Fuerza bruta       -> 429 tras agotar la ráfaga del rate limit de login
set -euo pipefail
cd "$(dirname "$0")/.."

PORT=18090
PASS="secreta-de-prueba-123"
LOG="$(mktemp)"

fail() { echo "FALLO: $*" >&2; echo "--- log del control plane ---"; tail -30 "$LOG"; exit 1; }

echo ">> compilando control plane"
GOTOOLCHAIN=local go build -o bin/controlplane ./cmd/controlplane

echo ">> arrancando con login local (usuario admin)"
ATLAS_ADMIN_PASSWORD="$PASS" ./bin/controlplane --addr ":$PORT" >"$LOG" 2>&1 &
CP_PID=$!
trap 'kill $CP_PID 2>/dev/null || true; rm -f "$LOG"' EXIT
for i in $(seq 1 50); do
  curl -sf "http://localhost:$PORT/healthz" >/dev/null 2>&1 && break
  sleep 0.2
done

echo ">> 1) sin token: /v1/topology debe dar 401"
code=$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:$PORT/v1/topology")
[ "$code" = "401" ] || fail "esperaba 401 sin token, obtuve $code"

echo ">> 2) contraseña mala: 401"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://localhost:$PORT/v1/login" \
  -H 'Content-Type: application/json' -d '{"username":"admin","password":"nope"}')
[ "$code" = "401" ] || fail "esperaba 401 con contraseña mala, obtuve $code"

echo ">> 3) contraseña buena: emite token"
resp=$(curl -sf -X POST "http://localhost:$PORT/v1/login" \
  -H 'Content-Type: application/json' -d "{\"username\":\"admin\",\"password\":\"$PASS\"}")
TOKEN=$(echo "$resp" | grep -o '"token":"[^"]*"' | cut -d'"' -f4)
[ -n "$TOKEN" ] || fail "el login no devolvió token: $resp"
echo "$resp" | grep -q '"user":"admin"' || fail "el login no devolvió el usuario: $resp"

echo ">> 4) con token: /v1/topology y /v1/audit responden 200"
code=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" \
  "http://localhost:$PORT/v1/topology")
[ "$code" = "200" ] || fail "esperaba 200 con token, obtuve $code"
audit=$(curl -sf -H "Authorization: Bearer $TOKEN" "http://localhost:$PORT/v1/audit")

echo ">> 5) token manipulado: 401"
code=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer ${TOKEN}x" \
  "http://localhost:$PORT/v1/topology")
[ "$code" = "401" ] || fail "esperaba 401 con token manipulado, obtuve $code"

echo ">> 6) la auditoría registra los intentos de login"
echo "$audit" | grep -q '"event":"auth.login"' || fail "no hay eventos auth.login en la auditoría"
echo "$audit" | grep -q 'FALLIDO' || fail "el intento fallido no quedó auditado"

echo ">> 7) usuarios desde la GUI: crear viewer, RBAC, borrar"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://localhost:$PORT/v1/users" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"username":"ana","password":"clave-de-ana-1","role":"viewer"}')
[ "$code" = "201" ] || fail "crear usuario: esperaba 201, obtuve $code"
curl -sf -H "Authorization: Bearer $TOKEN" "http://localhost:$PORT/v1/users" \
  | grep -q '"username":"ana"' || fail "ana no aparece en la lista de usuarios"

ANA=$(curl -sf -X POST "http://localhost:$PORT/v1/login" -H 'Content-Type: application/json' \
  -d '{"username":"ana","password":"clave-de-ana-1"}' | grep -o '"token":"[^"]*"' | cut -d'"' -f4)
[ -n "$ANA" ] || fail "ana no pudo iniciar sesión"
code=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $ANA" \
  "http://localhost:$PORT/v1/topology")
[ "$code" = "200" ] || fail "ana (viewer) debería LEER: esperaba 200, obtuve $code"
code=$(curl -s -o /dev/null -w '%{http_code}' -X PUT -H "Authorization: Bearer $ANA" \
  -H 'Content-Type: application/json' -d '{"note":"x"}' "http://localhost:$PORT/v1/annotations/test")
[ "$code" = "403" ] || fail "ana (viewer) NO debería escribir: esperaba 403, obtuve $code"
code=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $ANA" \
  "http://localhost:$PORT/v1/users")
[ "$code" = "403" ] || fail "ana (viewer) no debería gestionar usuarios: esperaba 403, obtuve $code"

code=$(curl -s -o /dev/null -w '%{http_code}' -X DELETE -H "Authorization: Bearer $TOKEN" \
  "http://localhost:$PORT/v1/users/ana")
[ "$code" = "200" ] || fail "borrar usuario: esperaba 200, obtuve $code"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://localhost:$PORT/v1/login" \
  -H 'Content-Type: application/json' -d '{"username":"ana","password":"clave-de-ana-1"}')
[ "$code" = "401" ] || fail "ana borrada aún puede entrar: esperaba 401, obtuve $code"

echo ">> 8) fuerza bruta: el rate limit corta con 429"
got429=no
for i in $(seq 1 12); do
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://localhost:$PORT/v1/login" \
    -H 'Content-Type: application/json' -d '{"username":"admin","password":"nope"}')
  [ "$code" = "429" ] && got429=yes && break
done
[ "$got429" = "yes" ] || fail "nunca llegó el 429 del rate limit de login"

echo
echo "OK: login local verificado (cerrado sin sesión, auditado y con rate limit)"
