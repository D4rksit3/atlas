#!/usr/bin/env bash
# Prueba de extremo a extremo del colector de ENLACES (Hubble) contra un clúster
# REAL con Cilium. Levanta kind SIN CNI, instala Cilium + Hubble Relay, despliega
# cargas que se hablan entre sí (web -> api, web -> db), corre el agente en modo
# kube+hubble y verifica que los enlaces observados aparecen en la topología.
# Limpia todo al terminar.
#
# Requisitos: docker, kind, kubectl, cilium (CLI), go 1.22+.
#   ./scripts/test-hubble.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER=atlas-hubble
CILIUM_VERSION=1.15.5
WORKDIR="$(mktemp -d)"
CP_PID=""; AGENT_PID=""; PF_PID=""

log()  { printf '\033[1;36m▶ %s\033[0m\n' "$*"; }
ok()   { printf '\033[1;32m✓ %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

cleanup() {
  [ -n "$AGENT_PID" ] && kill "$AGENT_PID" 2>/dev/null || true
  [ -n "$CP_PID" ]    && kill "$CP_PID"    2>/dev/null || true
  [ -n "$PF_PID" ]    && kill "$PF_PID"    2>/dev/null || true
  kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

for bin in docker kind kubectl cilium go; do
  command -v "$bin" >/dev/null || fail "falta '$bin' en el PATH"
done

log "creando clúster kind SIN CNI ($CLUSTER)…"
cat > "$WORKDIR/kind.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: $CLUSTER
networking:
  disableDefaultCNI: true
  kubeProxyMode: none
nodes:
  - role: control-plane
  - role: worker
  - role: worker
EOF
kind create cluster --config "$WORKDIR/kind.yaml" >/dev/null
export KUBECONFIG="$HOME/.kube/config"

log "instalando Cilium $CILIUM_VERSION + Hubble Relay…"
cilium install --version "$CILIUM_VERSION" \
  --set kubeProxyReplacement=true \
  --set hubble.enabled=true \
  --set hubble.relay.enabled=true >/dev/null
cilium status --wait --wait-duration 4m >/dev/null
ok "Cilium + Hubble listos"

log "desplegando cargas que se hablan entre sí…"
kubectl apply -f - >/dev/null <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata: { name: api, labels: { app: api } }
spec:
  replicas: 2
  selector: { matchLabels: { app: api } }
  template:
    metadata: { labels: { app: api } }
    spec: { containers: [{ name: api, image: nginx:alpine }] }
---
apiVersion: v1
kind: Service
metadata: { name: api }
spec: { selector: { app: api }, ports: [{ port: 80 }] }
---
apiVersion: apps/v1
kind: Deployment
metadata: { name: db, labels: { app: db } }
spec:
  replicas: 1
  selector: { matchLabels: { app: db } }
  template:
    metadata: { labels: { app: db } }
    spec: { containers: [{ name: db, image: nginx:alpine }] }
---
apiVersion: v1
kind: Service
metadata: { name: db }
spec: { selector: { app: db }, ports: [{ port: 5432, targetPort: 80 }] }
---
apiVersion: apps/v1
kind: Deployment
metadata: { name: web, labels: { app: web } }
spec:
  replicas: 3
  selector: { matchLabels: { app: web } }
  template:
    metadata: { labels: { app: web } }
    spec:
      containers:
        - name: web
          image: alpine/curl:latest
          command: ["sh","-c","while true; do curl -s -o /dev/null http://api; curl -s -o /dev/null http://db:5432 || true; sleep 1; done"]
EOF
kubectl rollout status deployment/web --timeout=120s >/dev/null
kubectl rollout status deployment/api --timeout=120s >/dev/null
ok "web -> api / web -> db generando tráfico"

log "port-forward a hubble-relay…"
kubectl -n kube-system port-forward svc/hubble-relay 4245:80 >/dev/null 2>&1 &
PF_PID=$!
sleep 4

log "compilando y arrancando control plane + agente (kube+hubble)…"
cd "$ROOT"
go build -o "$WORKDIR/controlplane" ./cmd/controlplane
go build -o "$WORKDIR/agent" ./cmd/agent
PORT=39080; while ss -ltn 2>/dev/null | grep -q ":$PORT "; do PORT=$((PORT+1)); done
ATLAS_ADDR=":$PORT" "$WORKDIR/controlplane" >"$WORKDIR/cp.log" 2>&1 &
CP_PID=$!
sleep 1
"$WORKDIR/agent" --collector kube --links hubble --hubble-server localhost:4245 \
  --name "kind cilium" --provider onprem --control-plane "http://localhost:$PORT" \
  >"$WORKDIR/agent.log" 2>&1 &
AGENT_PID=$!

log "esperando a que Hubble acumule flujos y el mapa los muestre…"
LINKS=0
for _ in $(seq 1 20); do
  sleep 3
  LINKS=$(curl -s "http://localhost:$PORT/v1/topology" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d['clusters'][0]['snapshot']['links'] or []) if d['clusters'] else 0)" 2>/dev/null || echo 0)
  [ "$LINKS" -ge 1 ] && break
done
[ "$LINKS" -ge 1 ] || fail "no aparecieron enlaces en la topología"

echo ""; echo "  Enlaces observados por Hubble en el mapa:"
curl -s "http://localhost:$PORT/v1/topology" | python3 -c "
import sys,json
for l in json.load(sys.stdin)['clusters'][0]['snapshot']['links']:
    print(f\"    {l['from']} -> {l['to']}\")"

# Verificamos que el enlace clave web->api esté presente.
HAS_WEB_API=$(curl -s "http://localhost:$PORT/v1/topology" | python3 -c "import sys,json;print(any(l['from']=='web' and l['to']=='api' for l in json.load(sys.stdin)['clusters'][0]['snapshot']['links']))")
[ "$HAS_WEB_API" = "True" ] || fail "esperaba el enlace web->api"
ok "enlace real web -> api presente en el mapa"

printf '\n\033[1;32m════════════════════════════════════════════\n'
printf '  TODO OK — colector Hubble verificado E2E\n'
printf '════════════════════════════════════════════\033[0m\n'
