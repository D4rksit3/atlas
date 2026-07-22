#!/usr/bin/env bash
# Prueba E2E de PUBLICAR SERVICIOS: instalar ingress-nginx y cert-manager desde la
# GUI (ambos charts de Helm, vía el SDK compilado en el agente). Para cada uno
# comprueba:
#   1) la acción install llega a 'done',
#   2) se crea un RELEASE de Helm de verdad (secret sh.helm.release...),
#   3) el controlador queda Ready (en k3d sí arrancan),
#   4) Atlas lo detecta como "instalado" (installedAddons en la topología).
# Y como cierre, crea un ClusterIssuer (acción 'issuer') desde la GUI y verifica
# que cert-manager lo registra en ACME staging y lo deja Ready.
# Con esto Atlas monta la cadena de publicación completa (IP → ruta HTTP(S) → TLS).
# Limpia todo al terminar.
#
# Requisitos: docker, k3d, kubectl, go 1.22+, curl, python3, internet.
#   ./scripts/test-ingress.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER=atlas-ingress
WORKDIR="$(mktemp -d)"
CP_PID=""; AGENT_PID=""; PORT=""

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

for bin in docker k3d kubectl go curl python3; do command -v "$bin" >/dev/null || fail "falta '$bin'"; done

# install_addon KEY: encola la acción install y espera a 'done' (o falla).
install_addon() {
  local key="$1"
  local id
  id=$(curl -s -X POST "http://localhost:$PORT/v1/clusters/prod-k3s/actions" \
        -H 'Content-Type: application/json' -d "{\"kind\":\"install\",\"addon\":\"$key\"}" \
        | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
  local s="?"
  for _ in $(seq 1 60); do
    sleep 4
    s=$(curl -s "http://localhost:$PORT/v1/clusters/prod-k3s/actions" \
         | python3 -c "import sys,json;a=[x for x in json.load(sys.stdin) if x['id']=='$id'];print(a[0]['status'] if a else '?')")
    [ "$s" = "done" ] && return 0
    [ "$s" = "error" ] && {
      curl -s "http://localhost:$PORT/v1/clusters/prod-k3s/actions" \
        | python3 -c "import sys,json;print([a.get('error') for a in json.load(sys.stdin) if a['id']=='$id'])" >&2
      fail "instalar $key falló"
    }
  done
  fail "la instalación de $key no llegó a done (estado: $s)"
}

# atlas_detecta KEY NS WORKLOAD: espera a que Atlas marque el complemento instalado.
atlas_detecta() {
  local key="$1" ns="$2" wl="$3" seen=""
  for _ in $(seq 1 15); do
    sleep 3
    seen=$(curl -s "http://localhost:$PORT/v1/topology" | python3 -c "
import sys,json
c=json.load(sys.stdin)['clusters'][0]
print(any(w['namespace']=='$ns' and '$wl' in w['name'] for w in c['snapshot']['workloads']))" 2>/dev/null)
    [ "$seen" = "True" ] && return 0
  done
  fail "Atlas no detecta $key ($ns/$wl)"
}

log "clúster + control plane + agente…"
k3d cluster create "$CLUSTER" --agents 1 --wait >/dev/null 2>&1
export KUBECONFIG="$HOME/.kube/config"
cd "$ROOT"
go build -o "$WORKDIR/controlplane" ./cmd/controlplane
go build -o "$WORKDIR/agent" ./cmd/agent
PORT=39700; while ss -ltn 2>/dev/null | grep -q ":$PORT "; do PORT=$((PORT+1)); done
ATLAS_ADDR=":$PORT" "$WORKDIR/controlplane" >"$WORKDIR/cp.log" 2>&1 &
CP_PID=$!
sleep 1
"$WORKDIR/agent" --collector kube --name "prod k3s" --provider onprem --cluster-id prod-k3s --control-plane "http://localhost:$PORT" >"$WORKDIR/agent.log" 2>&1 &
AGENT_PID=$!
for _ in $(seq 1 10); do sleep 1; curl -sf "http://localhost:$PORT/v1/topology" | grep -q prod-k3s && break; done

# ---- ingress-nginx ----
log "instalar ingress-nginx (Helm) desde la GUI…"
install_addon ingress-nginx
ok "acción install (ingress-nginx) en 'done'"
kubectl -n ingress-nginx get secret -l owner=helm 2>/dev/null | grep -q "sh.helm.release.v1.ingress-nginx" \
  || fail "no se creó el release de Helm de ingress-nginx"
ok "release de Helm creado (ingress-nginx)"
kubectl -n ingress-nginx rollout status deploy/ingress-nginx-controller --timeout=180s >/dev/null 2>&1 \
  || fail "el controlador de ingress-nginx no quedó Ready"
ok "ingress-nginx-controller Ready (recibe tráfico externo)"
atlas_detecta ingress-nginx ingress-nginx ingress-nginx-controller
ok "Atlas detecta ingress-nginx como instalado"

# ---- cert-manager ----
log "instalar cert-manager (Helm, con sus CRDs) desde la GUI…"
install_addon cert-manager
ok "acción install (cert-manager) en 'done'"
kubectl -n cert-manager get secret -l owner=helm 2>/dev/null | grep -q "sh.helm.release.v1.cert-manager" \
  || fail "no se creó el release de Helm de cert-manager"
ok "release de Helm creado (cert-manager)"
kubectl get crd clusterissuers.cert-manager.io >/dev/null 2>&1 \
  || fail "cert-manager no instaló sus CRDs (crds.enabled no se aplicó)"
ok "CRDs de cert-manager instaladas (ClusterIssuer disponible)"
kubectl -n cert-manager rollout status deploy/cert-manager --timeout=180s >/dev/null 2>&1 \
  || fail "cert-manager no quedó Ready"
ok "cert-manager Ready (puede emitir TLS)"
atlas_detecta cert-manager cert-manager cert-manager
ok "Atlas detecta cert-manager como instalado"

# ---- ClusterIssuer (acción issuer) ----
log "crear un emisor TLS (ClusterIssuer ACME staging) desde la GUI…"
IID=$(curl -s -X POST "http://localhost:$PORT/v1/clusters/prod-k3s/actions" \
  -H 'Content-Type: application/json' \
  -d '{"kind":"issuer","issuer":{"email":"ops@ich.edu.pe","environment":"staging"}}' \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
s="?"
for _ in $(seq 1 30); do
  sleep 3
  s=$(curl -s "http://localhost:$PORT/v1/clusters/prod-k3s/actions" \
       | python3 -c "import sys,json;a=[x for x in json.load(sys.stdin) if x['id']=='$IID'];print(a[0]['status'] if a else '?')")
  [ "$s" = "done" ] && break
  [ "$s" = "error" ] && { curl -s "http://localhost:$PORT/v1/clusters/prod-k3s/actions" | python3 -c "import sys,json;print([a.get('error') for a in json.load(sys.stdin) if a['id']=='$IID'])" >&2; fail "crear el emisor falló"; }
done
[ "$s" = "done" ] || fail "la acción issuer no llegó a done (estado: $s)"
ok "acción issuer en 'done'"

kubectl get clusterissuer letsencrypt-staging >/dev/null 2>&1 || fail "no se creó el ClusterIssuer"
ok "ClusterIssuer letsencrypt-staging creado"

# El servidor ACME debe ser el vetado de staging (no una URL arbitraria).
srv=$(kubectl get clusterissuer letsencrypt-staging -o jsonpath='{.spec.acme.server}')
[ "$srv" = "https://acme-staging-v02.api.letsencrypt.org/directory" ] \
  || fail "el servidor ACME es $srv (esperaba el de staging vetado)"
ok "servidor ACME = staging vetado (derivado del entorno, no de la GUI)"

# cert-manager registra la cuenta ACME y marca el emisor Ready (necesita internet).
kubectl wait --for=condition=Ready clusterissuer/letsencrypt-staging --timeout=120s >/dev/null 2>&1 \
  || fail "el ClusterIssuer no quedó Ready (¿sin salida a ACME staging?)"
ok "ClusterIssuer Ready — cert-manager registró la cuenta ACME (listo para emitir TLS)"

printf '\n\033[1;32m═══════════════════════════════════════════\n'
printf '  TODO OK — publicar servicios: ingress-nginx + cert-manager + ClusterIssuer, E2E\n'
printf '═══════════════════════════════════════════\033[0m\n'
