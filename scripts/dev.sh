#!/usr/bin/env bash
#
# dev.sh — arranca TODO el stack de Atlas en local con un solo comando:
#   control plane + 3 agentes de ejemplo (on-prem/AWS/OCI) + GUI.
#
# Elige un puerto libre para el control plane, apunta la GUI hacia él y limpia
# todos los procesos al salir (Ctrl-C). Pensado para "clonar y correr".
#
set -euo pipefail
cd "$(dirname "$0")/.."

command -v go   >/dev/null || { echo "❌ Falta Go 1.22+.  Instala: https://go.dev/dl"; exit 1; }
command -v node >/dev/null || { echo "❌ Falta Node 20+. Instala: https://nodejs.org"; exit 1; }

# --- puerto libre para el control plane ---
PORT="${ATLAS_PORT:-8080}"
while ss -ltn 2>/dev/null | grep -q ":$PORT "; do PORT=$((PORT + 1)); done
CP="http://localhost:$PORT"
echo "▸ Control plane -> $CP"

# --- build ---
echo "▸ Compilando binarios…"
go build -o bin/controlplane ./cmd/controlplane
go build -o bin/agent ./cmd/agent

PIDS=()
cleanup() {
  echo; echo "▸ Deteniendo todo…"
  for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done
}
trap cleanup EXIT INT TERM

# --- control plane ---
ATLAS_ADDR=":$PORT" ./bin/controlplane & PIDS+=("$!")
sleep 1

# --- agentes de ejemplo (uno por entorno) ---
./bin/agent --control-plane "$CP" --name "on-prem lab" --provider onprem --cluster-id onprem-lab & PIDS+=("$!")
./bin/agent --control-plane "$CP" --name "prod eks"    --provider aws    --cluster-id prod-eks --sample-workers 4 & PIDS+=("$!")
./bin/agent --control-plane "$CP" --name "prod oke"    --provider oci    --cluster-id prod-oke --sample-workers 2 & PIDS+=("$!")

# --- dependencias de la GUI (solo la primera vez) ---
if [ ! -d web/node_modules ]; then
  echo "▸ Instalando dependencias de la GUI…"
  (cd web && npm install --no-fund --no-audit)
fi

echo
echo "  ✅ Todo arriba:"
echo "     • Consola (GUI):   http://localhost:5173"
echo "     • API topología:   $CP/v1/topology"
echo "     • Métricas:        $CP/metrics"
echo "     Ctrl-C para detener todo."
echo

# --- GUI en primer plano (apuntando al puerto elegido) ---
ATLAS_CONTROL_PLANE="$CP" npm --prefix web run dev
