#!/usr/bin/env bash
# Prueba E2E del CATÁLOGO de complementos (todo-en-uno) desde la GUI. Verifica:
#   1) GET /v1/addons devuelve el catálogo (gitops, seguridad, redes, monitoreo),
#   2) instalar un complemento NUEVO (Kyverno, seguridad) funciona por el mismo
#      canal genérico, y aparece en la topología (detección).
# Limpia todo al terminar.
#
# Requisitos: docker, k3d, kubectl, go 1.22+, curl, internet.
#   ./scripts/test-addons.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER=atlas-addons
WORKDIR="$(mktemp -d)"
CP_PID=""; AGENT_PID=""

log()  { printf '\033[1;36m▶ %s\033[0m\n' "$*"; }
ok()   { printf '\033[1;32m✓ %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

cleanup() {
  [ -n "$AGENT_PID" ] && kill "$AGENT_PID" 2>/dev/null || true
  [ -n "$CP_PID" ]    && kill "$CP_PID"    2>/dev/null || true
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for bin in docker k3d kubectl go curl; do command -v "$bin" >/dev/null || fail "falta '$bin'"; done

log "clúster + control plane + agente…"
k3d cluster create "$CLUSTER" --agents 1 --wait >/dev/null 2>&1
export KUBECONFIG="$HOME/.kube/config"
cd "$ROOT"
go build -o "$WORKDIR/controlplane" ./cmd/controlplane
go build -o "$WORKDIR/agent" ./cmd/agent
PORT=39600; while ss -ltn 2>/dev/null | grep -q ":$PORT "; do PORT=$((PORT+1)); done
ATLAS_ADDR=":$PORT" "$WORKDIR/controlplane" >"$WORKDIR/cp.log" 2>&1 &
CP_PID=$!
sleep 1
"$WORKDIR/agent" --collector kube --name "prod k3s" --provider onprem --cluster-id prod-k3s --control-plane "http://localhost:$PORT" >"$WORKDIR/agent.log" 2>&1 &
AGENT_PID=$!
for _ in $(seq 1 10); do sleep 1; curl -sf "http://localhost:$PORT/v1/topology" | grep -q prod-k3s && break; done

log "1) catálogo /v1/addons…"
CAT=$(curl -s "http://localhost:$PORT/v1/addons")
for k in argocd kyverno metallb metrics-server; do echo "$CAT" | grep -q "\"$k\"" || fail "el catálogo no incluye $k"; done
CATS=$(echo "$CAT" | python3 -c "import sys,json;print(','.join(sorted({a['category'] for a in json.load(sys.stdin)})))")
ok "catálogo OK (categorías: $CATS)"

log "2) instalar Kyverno (seguridad) por el canal genérico…"
ID=$(curl -s -X POST "http://localhost:$PORT/v1/clusters/prod-k3s/actions" -H 'Content-Type: application/json' -d '{"kind":"install","addon":"kyverno"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
for _ in $(seq 1 40); do
  sleep 3
  s=$(curl -s "http://localhost:$PORT/v1/clusters/prod-k3s/actions" | python3 -c "import sys,json;a=[x for x in json.load(sys.stdin) if x['id']=='$ID'];print(a[0]['status'] if a else '?')")
  [ "$s" = "done" ] && break
  [ "$s" = "error" ] && { curl -s "http://localhost:$PORT/v1/clusters/prod-k3s/actions" | python3 -c "import sys,json;print([a.get('error') for a in json.load(sys.stdin) if a['id']=='$ID'])"; fail "instalar Kyverno falló"; }
done
[ "$s" = "done" ] || fail "la instalación de Kyverno no llegó a done"
kubectl -n kyverno get deploy kyverno-admission-controller >/dev/null 2>&1 || fail "no existe kyverno-admission-controller"
ok "Kyverno instalado (namespace kyverno + admission-controller)"

log "3) Atlas lo detecta en la topología…"
for _ in $(seq 1 10); do
  sleep 3
  SEEN=$(curl -s "http://localhost:$PORT/v1/topology" | python3 -c "import sys,json;c=json.load(sys.stdin)['clusters'][0];print(any(w['namespace']=='kyverno' and 'admission' in w['name'] for w in c['snapshot']['workloads']))" 2>/dev/null)
  [ "$SEEN" = "True" ] && break
done
[ "$SEEN" = "True" ] || fail "Atlas no ve Kyverno"
ok "Atlas ve Kyverno (detección de 'instalado')"

printf '\n\033[1;32m═══════════════════════════════════════════════\n'
printf '  TODO OK — catálogo de complementos (todo-en-uno), E2E\n'
printf '═══════════════════════════════════════════════\033[0m\n'
