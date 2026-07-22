#!/usr/bin/env bash
# Prueba E2E del ACOTADO DE EGRESS de la NetworkPolicy (deploy/networkpolicy.yaml).
# En un clúster real (k3d aplica egress), levanta un pod por componente con su
# etiqueta (app: atlas-web|atlas-controlplane|atlas-agent) y comprueba la matriz de
# salida permitida/denegada:
#   - web:          egress :443 a internet BLOQUEADO (solo DNS + control plane :8080).
#   - control plane: egress :443 PERMITIDO (OIDC) y :80 BLOQUEADO (solo 443/5432).
#   - agente:       egress :443 PERMITIDO (charts/manifiestos/API) y :80 a internet
#                   BLOQUEADO (el :80 solo se abre hacia kube-system/Hubble).
# El contraste web(:443 bloqueado) vs control plane(:443 permitido) demuestra que el
# egress es POR COMPONENTE, no global. Limpia todo al terminar.
#
# Requisitos: docker, k3d, kubectl, internet.
#   ./scripts/test-netpol.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER=atlas-netpol
FAILED=0

log()  { printf '\033[1;36m▶ %s\033[0m\n' "$*"; }
ok()   { printf '\033[1;32m✓ %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m✗ %s\033[0m\n' "$*" >&2; FAILED=1; }

cleanup() { k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true; }
trap cleanup EXIT

for bin in docker k3d kubectl; do command -v "$bin" >/dev/null || { echo "falta $bin" >&2; exit 1; }; done

log "clúster k3d (aplica egress NetworkPolicy)…"
k3d cluster create "$CLUSTER" --wait >/dev/null 2>&1
export KUBECONFIG="$HOME/.kube/config"

# Namespace + policies (sin desplegar Atlas: solo probamos el aislamiento).
kubectl create ns atlas-system >/dev/null 2>&1 || true
for i in $(seq 1 30); do kubectl -n atlas-system get sa default >/dev/null 2>&1 && break; sleep 1; done
kubectl apply -f "$ROOT/deploy/networkpolicy.yaml" >/dev/null
ok "NetworkPolicy aplicada (default-deny ingress + egress, allows acotados)"

# Un pod curl por componente, con la etiqueta que selecciona su policy.
for app in atlas-web atlas-controlplane atlas-agent; do
  kubectl -n atlas-system run "$app" --image=curlimages/curl --labels "app=$app" \
    --restart=Never --command -- sleep 600 >/dev/null
done
for app in atlas-web atlas-controlplane atlas-agent; do
  kubectl -n atlas-system wait --for=condition=Ready "pod/$app" --timeout=90s >/dev/null
done

# probe POD URL: 0 si curl conecta (permitido), !=0 si lo bloquean (deny/timeout).
probe() { kubectl -n atlas-system exec "$1" -- curl -sS --connect-timeout 6 --max-time 10 -o /dev/null "$2" >/dev/null 2>&1; }

# expect POD URL allow|deny DESC
expect() {
  local pod="$1" url="$2" want="$3" desc="$4"
  if probe "$pod" "$url"; then got=allow; else got=deny; fi
  if [ "$got" = "$want" ]; then ok "$desc → $got"; else fail "$desc → $got (esperaba $want)"; fi
}

log "matriz de egress…"
# web: sin salida a internet (solo DNS + control plane :8080).
expect atlas-web            https://raw.githubusercontent.com deny  "web :443 a internet"
# control plane: 443 (OIDC) sí, 80 no.
expect atlas-controlplane   https://raw.githubusercontent.com allow "control plane :443 (OIDC)"
expect atlas-controlplane   http://example.com                deny  "control plane :80 a internet"
# agente: 443 (charts/API) sí, 80 a internet no.
expect atlas-agent          https://raw.githubusercontent.com allow "agente :443 (charts/API)"
expect atlas-agent          http://example.com                deny  "agente :80 a internet"

if [ "$FAILED" -ne 0 ]; then
  printf '\n\033[1;31m✗ FALLÓ el acotado de egress.\033[0m\n' >&2
  exit 1
fi
printf '\n\033[1;32m✓ EGRESS ACOTADO OK: cada componente sale solo a donde debe.\033[0m\n'
