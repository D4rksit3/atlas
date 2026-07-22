#!/usr/bin/env bash
# E2E del panel de servicios + login en clúster: instala Atlas en k3d con el
# instalador (que ahora SIEMPRE crea el login), comprueba que la API queda
# cerrada, inicia sesión, publica un servicio con la acción 'expose' y verifica
# que el Ingress enruta de verdad y que el snapshot adopta la ruta publicada.
set -euo pipefail
cd "$(dirname "$0")/.."
CLUSTER="atlas-services-test" PORT=8089 DOMAIN="atlas.local" SVC_HOST="demo.atlas.local"
export KUBECONFIG="${KUBECONFIG:-$HOME/.kube/config}"

cleanup() { k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "== build imágenes (dev) =="
docker build -q -t atlas-controlplane:dev --target controlplane . >/dev/null
docker build -q -t atlas-web:dev -f web/Dockerfile . >/dev/null
docker build -q -t atlas-agent:dev --target agent . >/dev/null

echo "== k3d $CLUSTER ($PORT:80) =="
cleanup
k3d cluster create "$CLUSTER" --agents 0 -p "$PORT:80@loadbalancer" --wait >/dev/null
# Kubeconfig PROPIO del test: en este host puede convivir un Atlas en producción
# y ningún kubectl del test debe poder tocarlo.
export KUBECONFIG="$(k3d kubeconfig write "$CLUSTER")"
# El objeto Node puede tardar unos segundos en existir tras el --wait de k3d.
for i in $(seq 1 30); do
  [ -n "$(kubectl get nodes -o name 2>/dev/null)" ] && break
  sleep 2
done
kubectl wait --for=condition=Ready nodes --all --timeout=120s >/dev/null
k3d image import atlas-controlplane:dev atlas-web:dev atlas-agent:dev -c "$CLUSTER" >/dev/null

echo "== instalar (local, $DOMAIN) — el instalador crea el login =="
out=$(./scripts/install.sh --domain "$DOMAIN" --mode local --image-prefix "" --tag dev)
PASS=$(echo "$out" | grep 'contraseña:' | awk '{print $NF}')
[ -n "$PASS" ] || { echo "FALLO: el instalador no mostró la contraseña generada"; exit 1; }
kubectl -n atlas-system rollout status deploy/atlas-controlplane --timeout=90s >/dev/null
kubectl -n atlas-system rollout status deploy/atlas-web --timeout=90s >/dev/null
kubectl -n atlas-system rollout status deploy/atlas-agent --timeout=90s >/dev/null

echo "== servicio de prueba a publicar (demo-web) =="
kubectl create deployment demo-web --image=nginx:alpine >/dev/null
kubectl expose deployment demo-web --port=80 >/dev/null
kubectl rollout status deploy/demo-web --timeout=90s >/dev/null

fail=0
check() { # descripción, esperado, obtenido
  if [ "$2" = "$3" ]; then echo "  ✓ $1 ($3)"; else echo "  ✗ $1: esperado $2, obtenido $3"; fail=1; fi
}
api() { curl -s --resolve "$DOMAIN:$PORT:127.0.0.1" "$@" || true; }

echo "== esperar a que el Ingress (traefik) enrute la GUI =="
ok=no
for i in $(seq 1 60); do
  code=$(api -o /dev/null -w '%{http_code}' "http://$DOMAIN:$PORT/")
  [ "$code" = "200" ] && ok=yes && break
  sleep 3
done
[ "$ok" = "yes" ] || { echo "FALLO: la GUI nunca respondió por el dominio (último código: $code)"; exit 1; }

echo "== login obligatorio =="
code=$(api -o /dev/null -w '%{http_code}' "http://$DOMAIN:$PORT/v1/topology")
check "sin sesión la API responde 401" 401 "$code"
methods=$(api "http://$DOMAIN:$PORT/v1/authconfig")
echo "$methods" | grep -q '"local"' && check "authconfig anuncia login local" si si \
  || check "authconfig anuncia login local" si no
TOKEN=$(api -X POST -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$PASS\"}" "http://$DOMAIN:$PORT/v1/login" \
  | grep -o '"token":"[^"]*"' | cut -d'"' -f4)
[ -n "$TOKEN" ] && check "login con la contraseña del instalador" si si \
  || check "login con la contraseña del instalador" si no
AUTH=(-H "Authorization: Bearer $TOKEN")

echo "== esperar el registro del agente =="
CID=""
for i in $(seq 1 30); do
  CID=$(api "${AUTH[@]}" "http://$DOMAIN:$PORT/v1/topology" \
    | grep -o '"clusterId":"[^"]*"' | head -1 | cut -d'"' -f4)
  [ -n "$CID" ] && break
  sleep 2
done
[ -n "$CID" ] || { echo "FALLO: el agente nunca se registró"; kubectl -n atlas-system logs deploy/atlas-agent --tail=20; exit 1; }
echo "  clúster: $CID"

echo "== publicar demo-web via acción expose =="
code=$(api -o /dev/null -w '%{http_code}' -X POST "${AUTH[@]}" -H 'Content-Type: application/json' \
  -d "{\"kind\":\"expose\",\"expose\":{\"namespace\":\"default\",\"service\":\"demo-web\",\"port\":80,\"host\":\"$SVC_HOST\",\"ingressClass\":\"traefik\"}}" \
  "http://$DOMAIN:$PORT/v1/clusters/$CID/actions")
check "la acción expose se encola (202)" 202 "$code"

echo "== esperar el Ingress y verificar el enrutado =="
ok=no
for i in $(seq 1 30); do
  kubectl get ingress atlas-demo-web >/dev/null 2>&1 && ok=yes && break
  sleep 2
done
check "el agente creó el Ingress atlas-demo-web" yes "$ok"
sleep 3
code=$(curl -s -o /dev/null -w '%{http_code}' --resolve "$SVC_HOST:$PORT:127.0.0.1" "http://$SVC_HOST:$PORT/")
check "el servicio publicado responde por su dominio" 200 "$code"

echo "== el snapshot adopta la ruta publicada (panel de servicios) =="
ok=no
for i in $(seq 1 30); do
  api "${AUTH[@]}" "http://$DOMAIN:$PORT/v1/topology" | grep -q "\"host\":\"$SVC_HOST\"" && ok=yes && break
  sleep 2
done
check "la topología incluye el ingress publicado" yes "$ok"

echo "== la publicación queda auditada =="
api "${AUTH[@]}" "http://$DOMAIN:$PORT/v1/audit" | grep -q "publicar el servicio default/demo-web" \
  && check "auditoría de la publicación" si si || check "auditoría de la publicación" si no

[ "$fail" = 0 ] && echo "== OK ==" || { echo "== FALLO =="; exit 1; }
