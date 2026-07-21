# Seguridad

La seguridad es un objetivo central de Atlas, no un añadido. Este documento
describe el modelo de amenazas del scaffold actual, lo que **ya** está bien y lo
que **falta** antes de considerarlo apto para producción. Sé honesto con esto:
un panel que gobierna clústeres es un objetivo de alto valor.

## Principio de diseño: conexión saliente

El agente **siempre inicia** la conexión hacia el control plane. Ningún clúster
gestionado expone puertos de entrada a Atlas. Esto reduce drásticamente la
superficie de ataque: no hay endpoints del agente que exponer ni VPNs entre
entornos que administrar.

## Estado actual (scaffold) — sé consciente

| Aspecto | Estado | Qué falta para producción |
|---|---|---|
| Identidad del agente | ✅ **mTLS** con certificado por agente (+ token) | — |
| Rotación de certs | ✅ **hojas de vida corta** (`--days`, 90 por defecto) + **hot-reload sin reinicio** | revocación inmediata (CRL/OCSP); hoy la mitiga la caducidad corta |
| Aislamiento de red | ✅ **NetworkPolicy** default-deny de ingress en `atlas-system` | acotar egress (Postgres/OIDC/API); requiere CNI que aplique NetworkPolicy |
| Transporte | ✅ **TLS 1.3** (mTLS) cuando se configuran certs | forzarlo en producción (hoy HTTP si no hay certs) |
| AuthN/AuthZ de la GUI | ✅ **OIDC (PKCE) + RBAC** (viewer/operator) | grupos anidados, sesión/refresh, auditoría |
| Endpoints de acción (escalar/reiniciar) | ✅ protegidos: exigen rol **operator** | — |
| Auditoría | ✅ **rastro de quién hizo qué** (solicitó/ejecutó, con resultado) | exportar a un SIEM; inmutabilidad |
| Instalar complementos (ArgoCD) | ⚠️ **opt-in**: catálogo cerrado + versión fijada, pero RBAC amplio | ClusterRole a medida por complemento (no cluster-admin) |
| CORS | `*` por defecto | fija el origen: `--cors-origin https://tu-gui` |
| Límite de tamaño de cuerpo | ✅ 1 MiB | — |
| Timeouts del servidor | ✅ read/write | — |
| Rate limiting | ✅ **por IP** (`--rate-limit`, 20/s por defecto) | ajustar tras un proxy de confianza |
| Cabeceras de seguridad | ✅ nosniff, X-Frame-Options DENY, Referrer-Policy, HSTS (bajo TLS) | CSP en la GUI |
| Contenedores | ✅ distroless, no-root | escaneo de imágenes en CI (Trivy) |
| Secretos | ❌ no hay gestión | integrar con Secret manager / SOPS |

## mTLS agente ↔ control plane (implementado)

El agente presenta un **certificado de cliente** firmado por la CA de Atlas; el
control plane lo **exige y lo verifica** (`RequireAndVerifyClientCert`, TLS 1.3).
A la vez el agente verifica el certificado del servidor. Genera la PKI con la
herramienta incluida (sin dependencias):

```bash
make certs                                   # CA + servidor(localhost) + un agente
# o a mano:
go run ./cmd/atlas-certs bundle --out certs --hosts atlas-cp.example.com
go run ./cmd/atlas-certs client --out certs --name prod-eks   # un cert por agente

# control plane:
controlplane --tls-cert certs/server.crt --tls-key certs/server.key --tls-client-ca certs/ca.crt
# agente:
agent --control-plane https://atlas-cp.example.com \
  --tls-cert certs/prod-eks.crt --tls-key certs/prod-eks.key --tls-ca certs/ca.crt
```

Verificado E2E (`make test-mtls`): sin certificado → rechazado; certificado de
otra CA → rechazado; certificado válido → registra.

### Rotación de certificados (implementado)

Las hojas (servidor y agente) se emiten con **vida corta** — `atlas-certs … --days
N`, **90 días por defecto** — mientras que la CA sigue durando años. El control
plane y el agente **recargan la hoja en caliente**: `internal/mtls` relee el par
cert/key del disco cuando cambia (comparando mtime+size) y lo usa en el siguiente
handshake **sin reiniciar el proceso**. Así, cuando cert-manager renueva el Secret
montado (o reemites con `atlas-certs`), la rotación es transparente.

```bash
go run ./cmd/atlas-certs bundle --out certs --hosts atlas-cp.example.com --days 30
# reemitir cuando toque; el proceso en marcha lo recoge solo, sin downtime.
```

Verificado E2E (`make test-rotation`): un handshake TLS real sirve el certificado
nuevo tras rotarlo en disco sin reiniciar, y el CLI emite hojas cortas mientras la
CA permanece larga. **Pendiente:** revocación inmediata (CRL/OCSP); hoy la mitiga
la caducidad corta — una hoja comprometida deja de valer en ≤`--days`.

## Aislamiento de red (NetworkPolicy)

`deploy/networkpolicy.yaml` aplica **default-deny de ingress** en `atlas-system` y
abre solo lo imprescindible: la GUI (:8080, punto de entrada tras el Ingress y
protegida por OIDC), el control plane (:8080, solo desde la GUI y el agente del
mismo clúster) y el agente sin ingress alguno (coherente con el modelo saliente).
El egress se deja abierto a propósito (control plane → Postgres/OIDC, agente → API
de Kubernetes y DNS); acotarlo es el siguiente paso. Requiere un CNI que aplique
NetworkPolicy (Cilium, Calico; k3s/k3d de serie).

```bash
kubectl apply -f deploy/networkpolicy.yaml
```

## Cómo endurecerlo (orden recomendado)

1. ✅ **mTLS agente↔control plane** — hecho (ver arriba). El token sigue como
   defensa en profundidad, pero la identidad fuerte es el certificado.
2. ✅ **AuthN de la GUI + proteger las acciones** — hecho: OIDC (Authorization
   Code + PKCE) + RBAC (viewer/operator). Los endpoints de acción exigen rol
   `operator`. Configura `--oidc-issuer/--oidc-client-id/--rbac-operators`.
   Verificado E2E con `make test-oidc`.
3. ✅ **Registro de auditoría** — hecho: cada acción deja rastro de quién la
   solicitó y su resultado (`GET /v1/audit`, panel "Actividad" en la GUI).
   Verificado con `make test-audit`. Pendiente: refresh de sesión, grupos
   anidados, exportar la auditoría a un SIEM.
3. **Fija CORS** al dominio real de la GUI.
4. **Escaneo continuo**: Trivy sobre las imágenes y `govulncheck` sobre el
   código (ya está en CI) en cada PR.
5. ✅ **Rotación de certificados** — hecho: hojas de vida corta (`--days`) +
   hot-reload sin reinicio. Verificado con `make test-rotation`. Pendiente:
   revocación inmediata (CRL/OCSP).
6. ✅ **Aislamiento de red** — hecho: `deploy/networkpolicy.yaml` (default-deny
   de ingress). Pendiente: acotar egress.

## Reportar una vulnerabilidad

No abras un issue público para vulnerabilidades. Escribe a
`security@TU-DOMINIO` (reemplázalo) con los detalles y pasos de reproducción.
Objetivo de respuesta: 72 h.

> Nota: mientras el proyecto esté en fase de scaffolding, **no lo expongas a
> internet** sin completar al menos los puntos 1–4.
