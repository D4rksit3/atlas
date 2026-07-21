#!/usr/bin/env bash
# Prueba de extremo a extremo del colector KUBE contra un clúster REAL.
#
# Levanta un clúster kind de 3 nodos, despliega cargas de verdad, arranca el
# control plane + el agente en modo kube, y verifica que la topología del mapa
# coincide con lo que reporta kubectl. Limpia todo al terminar.
#
# Requisitos: docker, kind, kubectl, go 1.22+.
#   ./scripts/test-kube.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER=atlas-e2e
WORKDIR="$(mktemp -d)"
CP_PID=""; AGENT_PID=""

log()  { printf '\033[1;36m▶ %s\033[0m\n' "$*"; }
ok()   { printf '\033[1;32m✓ %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

cleanup() {
  [ -n "$AGENT_PID" ] && kill "$AGENT_PID" 2>/dev/null || true
  [ -n "$CP_PID" ]    && kill "$CP_PID"    2>/dev/null || true
  kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for bin in docker kind kubectl go; do
  command -v "$bin" >/dev/null || fail "falta '$bin' en el PATH"
done

log "creando clúster kind de 3 nodos ($CLUSTER)…"
cat > "$WORKDIR/kind.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: $CLUSTER
nodes:
  - role: control-plane
  - role: worker
  - role: worker
EOF
kind create cluster --config "$WORKDIR/kind.yaml" --wait 120s >/dev/null
export KUBECONFIG="$HOME/.kube/config"

log "desplegando cargas reales…"
kubectl create namespace data >/dev/null 2>&1 || true
kubectl create deployment web --image=nginx:alpine --replicas=3 >/dev/null
kubectl create deployment api --image=nginx:alpine --replicas=2 >/dev/null
kubectl -n data apply -f - >/dev/null <<'EOF'
apiVersion: apps/v1
kind: StatefulSet
metadata: { name: postgres, namespace: data }
spec:
  serviceName: postgres
  replicas: 1
  selector: { matchLabels: { app: postgres } }
  template:
    metadata: { labels: { app: postgres } }
    spec:
      containers:
        - name: postgres
          image: postgres:16-alpine
          env: [{ name: POSTGRES_PASSWORD, value: dev }]
EOF
kubectl rollout status deployment/web --timeout=120s >/dev/null
kubectl rollout status deployment/api --timeout=120s >/dev/null
kubectl -n data rollout status statefulset/postgres --timeout=120s >/dev/null
ok "3 nodos + web(3)/api(2)/postgres(1) listos"

log "compilando binarios…"
cd "$ROOT"
go build -o "$WORKDIR/controlplane" ./cmd/controlplane
go build -o "$WORKDIR/agent" ./cmd/agent

PORT=39080; while ss -ltn 2>/dev/null | grep -q ":$PORT "; do PORT=$((PORT+1)); done
log "control plane en :$PORT"
ATLAS_ADDR=":$PORT" "$WORKDIR/controlplane" >"$WORKDIR/cp.log" 2>&1 &
CP_PID=$!
sleep 1

log "arrancando agente en modo KUBE…"
"$WORKDIR/agent" --collector kube --name "kind e2e" --provider onprem \
  --control-plane "http://localhost:$PORT" >"$WORKDIR/agent.log" 2>&1 &
AGENT_PID=$!

# Esperar a que llegue un latido con snapshot.
for _ in $(seq 1 15); do
  sleep 2
  ONLINE=$(curl -s "http://localhost:$PORT/v1/topology" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d['clusters'][0]['snapshot']['nodes']) if d['clusters'] else 0)" 2>/dev/null || echo 0)
  [ "$ONLINE" = "3" ] && break
done

log "verificando la topología del mapa…"
TOPO=$(curl -s "http://localhost:$PORT/v1/topology")
NODES=$(echo "$TOPO" | python3 -c "import sys,json;print(len(json.load(sys.stdin)['clusters'][0]['snapshot']['nodes']))")
WEB=$(echo "$TOPO"   | python3 -c "import sys,json;print(next(w['replicas'] for w in json.load(sys.stdin)['clusters'][0]['snapshot']['workloads'] if w['name']=='web'))")
[ "$NODES" = "3" ] || fail "esperaba 3 nodos, vi $NODES"
[ "$WEB" = "3" ]   || fail "esperaba web=3 réplicas, vi $WEB"
ok "el mapa refleja el clúster real: 3 nodos, web=3"

log "prueba de mapa VIVO: escalando web a 5…"
kubectl scale deployment/web --replicas=5 >/dev/null
kubectl rollout status deployment/web --timeout=60s >/dev/null
sleep 12
WEB2=$(curl -s "http://localhost:$PORT/v1/topology" | python3 -c "import sys,json;print(next(w['replicas'] for w in json.load(sys.stdin)['clusters'][0]['snapshot']['workloads'] if w['name']=='web'))")
[ "$WEB2" = "5" ] || fail "tras escalar esperaba web=5, el mapa dice $WEB2"
ok "el mapa se actualizó solo: web=5"

printf '\n\033[1;32m════════════════════════════════════════\n'
printf '  TODO OK — colector kube verificado E2E\n'
printf '════════════════════════════════════════\033[0m\n'
