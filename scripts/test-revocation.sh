#!/usr/bin/env bash
# Prueba de extremo a extremo de la REVOCACIÓN INMEDIATA de certificados de Atlas.
# Comprueba dos cosas que juntas cortan el acceso de un agente al instante:
#   1) internal/mtls rechaza en un handshake TLS real a un cliente cuyo serial está
#      en la CRL, y lo hace recargando la CRL EN CALIENTE (sin reiniciar el
#      servidor) — el test de socket de Go.
#   2) el CLI atlas-certs revoke añade el serial de un cert a una CRL firmada por la
#      CA (verificable con openssl), acumulando sobre revocaciones previas.
# No necesita clúster. Limpia todo al terminar.
#
# Requisitos: go 1.22+, openssl.
#   ./scripts/test-revocation.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"
CERTS="$WORKDIR/certs"

log()  { printf '\033[1;36m▶ %s\033[0m\n' "$*"; }
ok()   { printf '\033[1;32m✓ %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT

cd "$ROOT"

log "1) un agente revocado se queda fuera en el SIGUIENTE handshake, sin reiniciar…"
go test ./internal/mtls/ -run 'Revocation' -count=1 >/dev/null \
  || fail "la revocación en caliente no funciona (test de socket)"
ok "la CRL corta el acceso del agente revocado en el acto; los demás siguen entrando"

log "2) el CLI emite una PKI y revoca un agente por nombre…"
go build -o "$WORKDIR/atlas-certs" ./cmd/atlas-certs
"$WORKDIR/atlas-certs" bundle --out "$CERTS" --hosts localhost,127.0.0.1 --days 90 >/dev/null
"$WORKDIR/atlas-certs" client --out "$CERTS" --name prod-eks --days 90 >/dev/null

# Serial del agente a revocar (en hex, como lo lista openssl).
serial_hex="$(openssl x509 -in "$CERTS/prod-eks.crt" -noout -serial | cut -d= -f2)"

"$WORKDIR/atlas-certs" revoke --out "$CERTS" --name prod-eks >/dev/null
[ -f "$CERTS/ca.crl" ] || fail "revoke no generó la CRL (ca.crl)"

# La CRL debe estar firmada por la CA y listar el serial del agente revocado.
openssl crl -in "$CERTS/ca.crl" -CAfile "$CERTS/ca.crt" -noout >/dev/null 2>&1 \
  || fail "la CRL no verifica contra la CA"
ok "CRL firmada por la CA y verificable con openssl"

crl_text="$(openssl crl -in "$CERTS/ca.crl" -noout -text)"
echo "$crl_text" | grep -iq "Serial Number: *$serial_hex" \
  || fail "el serial $serial_hex del agente revocado no aparece en la CRL"
ok "el serial del agente prod-eks ($serial_hex) figura como revocado"

log "3) revocar un segundo agente ACUMULA sobre la CRL (no la reemplaza)…"
"$WORKDIR/atlas-certs" revoke --out "$CERTS" --name agent >/dev/null
count="$(openssl crl -in "$CERTS/ca.crl" -noout -text | grep -ic 'Serial Number:')"
[ "$count" -eq 2 ] || fail "la CRL tiene $count revocaciones, esperaba 2 (¿no acumula?)"
ok "la CRL acumula: 2 agentes revocados"

# Revocar un serial ya revocado no debe duplicar ni crecer.
"$WORKDIR/atlas-certs" revoke --out "$CERTS" --name prod-eks | grep -iq "ya estaba revocado" \
  || fail "re-revocar no fue idempotente"
ok "re-revocar un serial ya revocado es idempotente"

printf '\n\033[1;32m✓ REVOCACIÓN OK: CRL firmada por la CA + corte en caliente sin reinicio.\033[0m\n'
