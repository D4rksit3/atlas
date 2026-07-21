# Publicar Atlas en tu dominio

Guía para exponer Atlas en tu dominio de forma segura. **Estos pasos los ejecutas
tú** en tu clúster y tu proveedor de DNS — desde tu máquina o tu CI, con tus
credenciales.

## Vía rápida: el instalador (pregunta dominio + local/público)

```bash
./scripts/install.sh                 # interactivo: pregunta dominio y modo
# o directo:
./scripts/install.sh --domain atlas.seguricloud.com --mode public \
  --image-prefix ghcr.io/TU-ORG --tag v0.1.0 \
  --oidc-issuer https://TU-IDP --oidc-client-id atlas-gui --operators ops@seguricloud.com
```

El instalador **pregunta el dominio y si el despliegue es `local` o `public`** y
deja todo coherente con esa elección:

| | **local** | **public** |
|---|---|---|
| Esquema / CORS | `http://<dominio>` | `https://<dominio>` |
| TLS | ninguno | cert-manager (`--issuer`, def. `letsencrypt-prod`) |
| Ingress class | `traefik` (k3d/k3s) | `nginx` |
| Uso | probar en tu máquina | producción con DNS real |

`--render-only` imprime los manifiestos sin aplicarlos (para revisarlos o versionarlos).
El resto de este documento explica cada pieza por si prefieres hacerlo a mano.

## Resumen

```
  Internet ──HTTPS──► Ingress (nginx + cert-manager) ──► atlas-web (nginx)
                          atlas.seguricloud.com              │ proxy /v1
                                                             ▼
                                                     atlas-controlplane
                                                        (OIDC + rate limit)
```

## 0) Requisitos

- Un clúster de Kubernetes con salida a internet.
- **Ingress Controller** (nginx recomendado) y su IP/LoadBalancer.
- **cert-manager** con un `ClusterIssuer` (p. ej. `letsencrypt-prod`).
- Acceso al **DNS** de `seguricloud.com`.
- Un **IdP OIDC** (Google, Okta, Keycloak…) para el login de la GUI.

## 1) Construir y publicar las imágenes

```bash
docker build -t ghcr.io/TU-ORG/atlas-controlplane:v0.1.0 --target controlplane .
docker build -t ghcr.io/TU-ORG/atlas-agent:v0.1.0        --target agent .
docker build -t ghcr.io/TU-ORG/atlas-web:v0.1.0 -f web/Dockerfile .
docker push ghcr.io/TU-ORG/atlas-{controlplane,agent,web}:v0.1.0
```

Ajusta el campo `image:` en `deploy/controlplane.yaml` y `deploy/web.yaml`.

## 2) DNS

Crea un registro apuntando a la IP de tu Ingress Controller:

```
atlas.seguricloud.com.   A   <IP-del-Ingress>
# (si expones agentes externos) atlas-cp.seguricloud.com A <IP>
```

## 3) Configurar el control plane (OIDC + CORS + producción)

En `deploy/controlplane.yaml`, descomenta/ajusta el env:

```yaml
env:
  - { name: ATLAS_CORS_ORIGIN, value: "https://atlas.seguricloud.com" }
  - { name: ATLAS_OIDC_ISSUER,    value: "https://TU-IDP" }
  - { name: ATLAS_OIDC_CLIENT_ID, value: "atlas-gui" }
  - { name: ATLAS_RBAC_OPERATORS, value: "ops@seguricloud.com" }
  - { name: ATLAS_STORE,          value: "postgres" }   # persistencia
  - { name: ATLAS_POSTGRES_DSN,   valueFrom: { secretKeyRef: { name: atlas-pg, key: dsn } } }
```

En tu IdP, registra la GUI como **cliente público** con
`redirect_uri = https://atlas.seguricloud.com` (la GUI usa su propio origen).

## 4) mTLS entre agente y control plane

```bash
make certs   # genera CA + server + un cert de agente en ./certs
# monta certs/server.* y certs/ca.crt en el control plane (Secret + --tls-*)
```

Para agentes de **otros** clústeres, usa el asistente **"+ Vincular clúster"** de
la GUI (genera el cert y el manifiesto) y expón el control plane con **SSL
passthrough** (ver `deploy/ingress.yaml`).

## 5) Desplegar

```bash
kubectl apply -f deploy/controlplane.yaml
kubectl apply -f deploy/web.yaml
kubectl apply -f deploy/ingress.yaml        # crea el cert TLS automáticamente
kubectl apply -f deploy/agent.yaml          # agente del clúster local (opcional)
```

Espera a que cert-manager emita el certificado:

```bash
kubectl -n atlas-system get certificate atlas-web-tls -w
```

Abre **https://atlas.seguricloud.com** → login OIDC → el mapa.

## 6) Endurecimiento (ya incluido / recomendado)

- ✅ **mTLS**, **OIDC+RBAC**, **auditoría**, **rate limiting** (`--rate-limit`),
  **cabeceras de seguridad** (HSTS bajo TLS).
- Recomendado: **NetworkPolicies** en `atlas-system`, escaneo de imágenes (Trivy)
  en CI, rotación de certificados, y exportar la auditoría a tu SIEM.

> Nota: si el control plane está detrás de un proxy/Ingress, el rate limiting por
> IP debería leer `X-Forwarded-For` de un proxy de confianza (ya se contempla).
