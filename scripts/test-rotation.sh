#!/usr/bin/env bash
# Prueba de extremo a extremo de la ROTACIÓN de certificados de Atlas.
# Comprueba dos cosas que juntas hacen viable la rotación sin reinicio:
#   1) el CLI atlas-certs emite hojas de vida CORTA con --days (la CA sigue larga),
#   2) internal/mtls recarga la hoja EN CALIENTE en un handshake TLS real cuando el
#      fichero cambia en disco (el test de socket de Go), sin reconstruir la config.
# No necesita clúster. Limpia todo al terminar.
#
# Requisitos: go 1.22+, openssl.
#   ./scripts/test-rotation.sh
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

log "1) hot-reload de la hoja en un handshake TLS real (test de socket de Go)…"
go test ./internal/mtls/ -run 'HotReload|OverSocket' -count=1 >/dev/null \
  || fail "el hot-reload del certificado no funciona"
ok "el control plane sirve el certificado nuevo tras rotarlo en disco, sin reiniciar"

log "2) el CLI emite hojas de vida corta con --days…"
go build -o "$WORKDIR/atlas-certs" ./cmd/atlas-certs
"$WORKDIR/atlas-certs" bundle --out "$CERTS" --hosts localhost,127.0.0.1 --days 7 >/dev/null

# La hoja (agente) debe caducar en ~7 días; la CA debe seguir siendo larga (años).
leaf_end="$(openssl x509 -in "$CERTS/agent.crt" -noout -enddate | cut -d= -f2)"
ca_end="$(openssl x509 -in "$CERTS/ca.crt" -noout -enddate | cut -d= -f2)"
now=$(date +%s)
leaf_days=$(( ( $(date -d "$leaf_end" +%s) - now ) / 86400 ))
ca_days=$(( ( $(date -d "$ca_end" +%s) - now ) / 86400 ))

[ "$leaf_days" -ge 5 ] && [ "$leaf_days" -le 8 ] \
  || fail "la hoja caduca en $leaf_days días, esperaba ~7 (--days no se aplicó)"
ok "hoja del agente válida $leaf_days días (rotación forzada)"

[ "$ca_days" -ge 3000 ] \
  || fail "la CA solo dura $ca_days días; debería durar años (no se rota con la hoja)"
ok "CA válida $ca_days días (estable; solo rotan las hojas)"

# Reemitir la hoja del servidor cambia el fichero: eso es lo que dispara el
# hot-reload verificado en el paso 1.
before="$(openssl x509 -in "$CERTS/server.crt" -noout -serial)"
"$WORKDIR/atlas-certs" server --out "$CERTS" --hosts localhost,127.0.0.1 --days 7 >/dev/null
after="$(openssl x509 -in "$CERTS/server.crt" -noout -serial)"
[ "$before" != "$after" ] || fail "reemitir el servidor no cambió el certificado"
ok "reemitir produce una hoja nueva (serial $before → $after) que el proceso recarga solo"

printf '\n\033[1;32m✓ ROTACIÓN OK: hojas cortas por CLI + hot-reload sin reinicio.\033[0m\n'
