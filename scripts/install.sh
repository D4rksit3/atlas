#!/usr/bin/env bash
# Instalador de Atlas. Pregunta el DOMINIO y si el despliegue es LOCAL o PÚBLICO,
# y deja la plataforma corriendo con el Ingress, CORS y TLS correctos para ese caso.
#
#   ./scripts/install.sh                      # interactivo (pregunta todo)
#   ./scripts/install.sh --domain atlas.local --mode local
#   ./scripts/install.sh --domain atlas.seguricloud.com --mode public \
#       --oidc-issuer https://idp --oidc-client-id atlas-gui --operators ops@seguricloud.com
#
# LOCAL   → HTTP, sin cert-manager, clase de ingress traefik (k3d/k3s). Te recuerda
#           el /etc/hosts. Ideal para probar en tu máquina.
# PÚBLICO → HTTPS con cert-manager (ClusterIssuer) + nginx. Requiere que el DNS del
#           dominio ya apunte a la IP del Ingress (según tú, ya está apuntado).
set -euo pipefail

DOMAIN="" MODE="" NS="atlas-system"
IMAGE_PREFIX="${ATLAS_IMAGE_PREFIX:-ghcr.io/atlasctl}" TAG="${ATLAS_TAG:-latest}"
INGRESS_CLASS="" ISSUER="letsencrypt-prod"
OIDC_ISSUER="" OIDC_CLIENT_ID="" OPERATORS="" WITH_AGENT="yes" APPLY="yes"

while [ $# -gt 0 ]; do
  case "$1" in
    --domain) DOMAIN="$2"; shift 2;;
    --mode) MODE="$2"; shift 2;;
    --image-prefix) IMAGE_PREFIX="$2"; shift 2;;
    --tag) TAG="$2"; shift 2;;
    --ingress-class) INGRESS_CLASS="$2"; shift 2;;
    --issuer) ISSUER="$2"; shift 2;;
    --oidc-issuer) OIDC_ISSUER="$2"; shift 2;;
    --oidc-client-id) OIDC_CLIENT_ID="$2"; shift 2;;
    --operators) OPERATORS="$2"; shift 2;;
    --no-agent) WITH_AGENT="no"; shift;;
    --render-only) APPLY="no"; shift;;   # solo imprime los manifiestos, no aplica
    -h|--help) grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0;;
    *) echo "opción desconocida: $1" >&2; exit 2;;
  esac
done

# --- Preguntas interactivas (solo lo que falte) ---------------------------------
if [ -z "$DOMAIN" ]; then
  read -rp "Dominio para Atlas (p. ej. atlas.seguricloud.com o atlas.local): " DOMAIN
fi
if [ -z "$MODE" ]; then
  read -rp "¿Despliegue [local] o [public]? " MODE
fi
[ -z "$DOMAIN" ] && { echo "hace falta un dominio" >&2; exit 2; }
case "$MODE" in
  local|public) ;;
  *) echo "modo inválido: '$MODE' (usa: local | public)" >&2; exit 2;;
esac

if [ "$MODE" = "public" ]; then
  SCHEME="https"; [ -z "$INGRESS_CLASS" ] && INGRESS_CLASS="nginx"
else
  SCHEME="http";  [ -z "$INGRESS_CLASS" ] && INGRESS_CLASS="traefik"
fi
CORS_ORIGIN="$SCHEME://$DOMAIN"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# Nombre completo de imagen (prefijo opcional: vacío = imagen local sin registro).
pfx="${IMAGE_PREFIX:+$IMAGE_PREFIX/}"
CP_IMG="${pfx}atlas-controlplane:$TAG"
WEB_IMG="${pfx}atlas-web:$TAG"
AGENT_IMG="${pfx}atlas-agent:$TAG"

emit() { [ "$APPLY" = "yes" ] && kubectl apply -f - || cat; }

echo ">> Instalando Atlas en $CORS_ORIGIN  (modo=$MODE, ingress=$INGRESS_CLASS)"
[ "$APPLY" = "yes" ] && kubectl create namespace "$NS" >/dev/null 2>&1 || true

# --- Control plane (imagen + CORS + OIDC por env) -------------------------------
# Reemplaza el marcador ATLAS_ENV_INJECT (la línea name/value de CORS) con el
# dominio real y, si se pidió, la config OIDC. Indentado a 12 espacios (lista env).
env_block="            - { name: ATLAS_CORS_ORIGIN, value: \"$CORS_ORIGIN\" }"
if [ -n "$OIDC_ISSUER" ]; then
  env_block="$env_block
            - { name: ATLAS_OIDC_ISSUER, value: \"$OIDC_ISSUER\" }
            - { name: ATLAS_OIDC_CLIENT_ID, value: \"$OIDC_CLIENT_ID\" }
            - { name: ATLAS_RBAC_OPERATORS, value: \"$OPERATORS\" }"
fi
sed -E "s#^( *)image: .*atlas-controlplane:.*#\1image: $CP_IMG#" \
    "$ROOT/deploy/controlplane.yaml" \
  | awk -v env="$env_block" '
      skip { skip=0; next }                              # descarta el value: viejo
      /- name: ATLAS_CORS_ORIGIN/ { print env; skip=1; next }
      { print }' \
  | emit

sed -E "s#^( *)image: .*atlas-web:.*#\1image: $WEB_IMG#" "$ROOT/deploy/web.yaml" | emit

if [ "$WITH_AGENT" = "yes" ]; then
  sed -E "s#^( *)image: .*atlas-agent:.*#\1image: $AGENT_IMG#" "$ROOT/deploy/agent.yaml" | emit
fi

# --- Ingress: TLS+cert-manager en público, HTTP plano en local ------------------
if [ "$MODE" = "public" ]; then
cat <<YAML | emit
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: atlas-web
  namespace: $NS
  annotations:
    cert-manager.io/cluster-issuer: $ISSUER
spec:
  ingressClassName: $INGRESS_CLASS
  tls:
    - hosts: [$DOMAIN]
      secretName: atlas-web-tls
  rules:
    - host: $DOMAIN
      http:
        paths:
          - { path: /, pathType: Prefix, backend: { service: { name: atlas-web, port: { number: 80 } } } }
YAML
else
cat <<YAML | emit
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: atlas-web
  namespace: $NS
spec:
  ingressClassName: $INGRESS_CLASS
  rules:
    - host: $DOMAIN
      http:
        paths:
          - { path: /, pathType: Prefix, backend: { service: { name: atlas-web, port: { number: 80 } } } }
YAML
fi

[ "$APPLY" = "no" ] && exit 0

echo
echo ">> Listo. Atlas quedará disponible en:  $CORS_ORIGIN"
if [ "$MODE" = "public" ]; then
  echo "   Esperando el certificado TLS (cert-manager)…"
  echo "   kubectl -n $NS get certificate atlas-web-tls -w"
else
  echo "   (local) añade esto a /etc/hosts si el dominio no resuelve:"
  echo "     127.0.0.1  $DOMAIN"
fi
echo "   kubectl -n $NS rollout status deploy/atlas-controlplane"
