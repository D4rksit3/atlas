#!/usr/bin/env bash
# Prueba de extremo a extremo del store en Postgres. Verifica las dos cosas que
# el store en memoria NO puede hacer:
#   1) MULTI-RÉPLICA: dos control planes contra la misma base ven el mismo estado.
#   2) PERSISTENCIA:  el estado sobrevive a reiniciar el control plane.
# Levanta su propio Postgres en Docker. Limpia todo al terminar.
#
# Requisitos: docker, go 1.22+, curl.
#   ./scripts/test-postgres.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"
PG=atlas-pg-test
PG_PORT=55440
DSN="postgres://postgres:dev@localhost:${PG_PORT}/atlas"
PIDS=()

log()  { printf '\033[1;36m▶ %s\033[0m\n' "$*"; }
ok()   { printf '\033[1;32m✓ %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

cleanup() {
  for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  docker rm -f "$PG" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

command -v docker >/dev/null || fail "falta docker"

# topología online de un control plane dado (True/False/vacío).
online() { curl -s "http://localhost:$1/v1/topology" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d['clusters'][0]['online'] if d['clusters'] else 'VACIO')" 2>/dev/null || echo err; }
workloads() { curl -s "http://localhost:$1/v1/topology" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d['clusters'][0]['snapshot']['workloads'] or []) if d['clusters'] else 0)" 2>/dev/null || echo 0; }

log "levantando Postgres en Docker…"
docker rm -f "$PG" >/dev/null 2>&1 || true
docker run -d --name "$PG" -e POSTGRES_PASSWORD=dev -e POSTGRES_DB=atlas -p ${PG_PORT}:5432 postgres:16-alpine >/dev/null
for _ in $(seq 1 30); do docker exec "$PG" pg_isready -U postgres >/dev/null 2>&1 && break; sleep 1; done
docker exec "$PG" pg_isready -U postgres >/dev/null 2>&1 || fail "Postgres no arrancó"
ok "Postgres listo"

log "compilando binarios…"
cd "$ROOT"
go build -o "$WORKDIR/controlplane" ./cmd/controlplane
go build -o "$WORKDIR/agent" ./cmd/agent

freeport() { local p=$1; while ss -ltn 2>/dev/null | grep -q ":$p "; do p=$((p+1)); done; echo "$p"; }

PA=$(freeport 39200)
log "control plane A (postgres) en :$PA + agente…"
ATLAS_ADDR=":$PA" ATLAS_STORE=postgres ATLAS_POSTGRES_DSN="$DSN" "$WORKDIR/controlplane" >"$WORKDIR/cpA.log" 2>&1 &
PIDS+=($!)
sleep 1.5
"$WORKDIR/agent" --control-plane "http://localhost:$PA" --name "prod eks" --provider aws --cluster-id prod-eks --sample-workers 4 >"$WORKDIR/agent.log" 2>&1 &
AGENT_PID=$!; PIDS+=($AGENT_PID)
for _ in $(seq 1 10); do sleep 1; [ "$(online "$PA")" = "True" ] && break; done
[ "$(online "$PA")" = "True" ] || fail "el agente no registró en CP-A"
ok "clúster registrado en CP-A (persistido en Postgres)"

PB=$(freeport 39300)
log "control plane B (segunda réplica, misma base) en :$PB…"
ATLAS_ADDR=":$PB" ATLAS_STORE=postgres ATLAS_POSTGRES_DSN="$DSN" "$WORKDIR/controlplane" >"$WORKDIR/cpB.log" 2>&1 &
PB_PID=$!; PIDS+=($PB_PID)
sleep 2
[ "$(online "$PB")" = "True" ] || fail "CP-B no ve el clúster (multi-réplica roto)"
ok "CP-B ve el mismo clúster sin que nadie le hable — MULTI-RÉPLICA"

WL=$(workloads "$PB")
log "matando agente + ambos control planes y arrancando uno NUEVO…"
kill "$AGENT_PID" "${PIDS[0]}" "$PB_PID" 2>/dev/null || true
sleep 2
PC=$(freeport 39400)
ATLAS_ADDR=":$PC" ATLAS_STORE=postgres ATLAS_POSTGRES_DSN="$DSN" "$WORKDIR/controlplane" >"$WORKDIR/cpC.log" 2>&1 &
PIDS+=($!)
sleep 2
STATE=$(online "$PC")
[ "$STATE" != "VACIO" ] && [ "$STATE" != "err" ] || fail "el clúster se perdió tras reiniciar (persistencia rota)"
WL2=$(workloads "$PC")
[ "$WL2" = "$WL" ] || fail "el snapshot no sobrevivió ($WL2 vs $WL cargas)"
ok "el clúster y su snapshot ($WL2 cargas) sobrevivieron al reinicio — PERSISTENCIA"

printf '\n\033[1;32m══════════════════════════════════════════════\n'
printf '  TODO OK — Postgres: multi-réplica + persistencia\n'
printf '══════════════════════════════════════════════\033[0m\n'
