# Desplegar Atlas dentro de Kubernetes

Guía para correr el **control plane** y la **GUI** de Atlas dentro de un clúster,
y conectar clústeres (el mismo u otros) con el **agente**.

> Verificado de extremo a extremo contra Kubernetes 1.30 (kind): control plane +
> GUI + agente in-cluster, con Atlas monitoreándose a sí mismo. Reprodúcelo con
> `make test-deploy`.

## Piezas y cómo se hablan

```
  Navegador ──► GUI (nginx)  ──/v1──►  Control Plane  ◄──registro/latidos──  Agente
                Service atlas-web       Service atlas-controlplane            (en cada clúster)
```

- El **control plane** solo sirve HTTP (registro de agentes, topología, métricas).
  **No** toca la API de Kubernetes → no necesita RBAC.
- La **GUI** (nginx) sirve la SPA y hace de proxy de `/v1` hacia el Service del
  control plane, en el mismo namespace.
- El **agente** *marca hacia casa*: abre una conexión **saliente** al control
  plane. Nunca al revés. Por eso funciona detrás de NAT/firewall sin abrir puertos.

## 1) Construir y publicar las imágenes

```bash
docker build -t ghcr.io/TU_ORG/atlas-controlplane:TAG --target controlplane .
docker build -t ghcr.io/TU_ORG/atlas-agent:TAG        --target agent .
docker build -t ghcr.io/TU_ORG/atlas-web:TAG -f web/Dockerfile .
docker push ghcr.io/TU_ORG/atlas-{controlplane,agent,web}:TAG
```

Luego ajusta el campo `image:` en `deploy/controlplane.yaml`, `deploy/web.yaml`
y `deploy/agent.yaml`.

## 2) Desplegar control plane + GUI

```bash
kubectl apply -f deploy/controlplane.yaml   # Namespace atlas-system + Deployment + Service
kubectl apply -f deploy/web.yaml            # GUI (nginx) + ConfigMap + Service
kubectl -n atlas-system rollout status deploy/atlas-controlplane
kubectl -n atlas-system rollout status deploy/atlas-web
```

Ver la GUI sin Ingress todavía:

```bash
kubectl -n atlas-system port-forward svc/atlas-web 8088:80
# abre http://localhost:8088
```

## 3) Conectar un clúster con el agente

**Mismo clúster** (el agente alcanza el control plane por DNS de Service):

```bash
kubectl apply -f deploy/agent.yaml
# ATLAS_CONTROL_PLANE = http://atlas-controlplane.atlas-system:8080
```

Para leer el clúster de verdad, el agente ya trae `--collector=kube`. Para las
conexiones entre servicios, descomenta `--links=hubble` (requiere Cilium+Hubble;
ver [README](../README.md#conexiones-reales-entre-servicios-hubble)).

**Otro clúster / otro entorno** (on-prem, AWS, OCI): el agente necesita alcanzar
el control plane desde fuera. Expón el control plane con el **Ingress** (con TLS)
que viene comentado en `deploy/controlplane.yaml`, y en ese agente pon:

```
ATLAS_CONTROL_PLANE = https://atlas-cp.tu-dominio.com
```

No hace falta VPN ni abrir puertos de entrada en el clúster remoto: la conexión
la inicia el agente.

## 4) Exponer al exterior (Ingress + TLS)

Ambos manifiestos traen un `Ingress` comentado (nginx + cert-manager). Descoméntalo
y ajusta el `host`. Recomendado:

- GUI en `https://atlas.tu-dominio.com`
- Control plane en `https://atlas-cp.tu-dominio.com` (solo si tienes agentes externos)

## Notas de producción

- **Persistencia y HA (Postgres)**: por defecto el store es **en memoria** (1
  réplica, se pierde al reiniciar). Para persistir y escalar a varias réplicas,
  usa Postgres: guarda el DSN en un Secret y pon `ATLAS_STORE=postgres` +
  `ATLAS_POSTGRES_DSN` (ver el env comentado en `deploy/controlplane.yaml`).
  Con Postgres, subir `replicas` es seguro — el estado vive en la base, no en el
  proceso. Verificado con `make test-postgres` (multi-réplica + persistencia).
- **CORS**: en `deploy/controlplane.yaml`, fija `ATLAS_CORS_ORIGIN` al origen de
  tu GUI en vez de `*`.
- **Auth de la GUI (OIDC + RBAC)**: registra Atlas como aplicación en tu IdP
  (client público, redirect_uri = URL de la GUI) y configura en el control plane
  `ATLAS_OIDC_ISSUER`, `ATLAS_OIDC_CLIENT_ID` y `ATLAS_RBAC_OPERATORS` (ver el env
  comentado en `deploy/controlplane.yaml`). Sin esto, **la GUI no pide login** —
  no la expongas así. Verificado con `make test-oidc`.
- **mTLS**: activa la autenticación por certificado entre agente y control plane
  (ver [SECURITY.md](../SECURITY.md#mtls-agente--control-plane-implementado)).
  In-cluster: genera la PKI con `make certs`, crea Secrets y móntalos:

  ```bash
  kubectl -n atlas-system create secret generic atlas-cp-tls \
    --from-file=certs/server.crt --from-file=certs/server.key --from-file=certs/ca.crt
  kubectl -n atlas-system create secret generic atlas-agent-tls \
    --from-file=certs/agent.crt --from-file=certs/agent.key --from-file=certs/ca.crt
  ```

  Luego monta cada Secret y pasa `--tls-cert/--tls-key/--tls-client-ca` (control
  plane) y `--tls-cert/--tls-key/--tls-ca` (agente), con la URL en `https://`.
- Los pods corren **no-root**, con `readOnlyRootFilesystem` y `cap drop ALL`.
