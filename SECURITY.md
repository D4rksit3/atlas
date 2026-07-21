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
| Identidad del agente | ✅ **mTLS** con certificado por agente (+ token) | rotación/expiración corta y revocación (CRL/OCSP) |
| Transporte | ✅ **TLS 1.3** (mTLS) cuando se configuran certs | forzarlo en producción (hoy HTTP si no hay certs) |
| AuthN/AuthZ de la GUI | ❌ ninguna | OIDC/SSO + RBAC por usuario |
| Endpoints de acción (escalar/reiniciar) | ⚠️ **sin auth** | proteger con la auth de la GUI antes de exponer |
| CORS | `*` por defecto | fija el origen: `--cors-origin https://tu-gui` |
| Límite de tamaño de cuerpo | ✅ 1 MiB | — |
| Timeouts del servidor | ✅ read/write | añadir rate-limiting por agente |
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
otra CA → rechazado; certificado válido → registra. **Pendiente:** rotación con
expiración corta y revocación (CRL/OCSP); hoy los certs de hoja duran 1 año.

## Cómo endurecerlo (orden recomendado)

1. ✅ **mTLS agente↔control plane** — hecho (ver arriba). El token sigue como
   defensa en profundidad, pero la identidad fuerte es el certificado.
2. **AuthN de la GUI + proteger las acciones**: OIDC + RBAC. **Urgente**: la GUI
   ya puede escalar/reiniciar cargas (`POST /v1/clusters/{id}/actions`) y esos
   endpoints **no tienen auth**. No expongas el control plane hasta cerrarlos.
3. **Fija CORS** al dominio real de la GUI.
4. **Escaneo continuo**: Trivy sobre las imágenes y `govulncheck` sobre el
   código (ya está en CI) en cada PR.
5. **Rotación de certificados**: expiración corta + emisión automática.

## Reportar una vulnerabilidad

No abras un issue público para vulnerabilidades. Escribe a
`security@TU-DOMINIO` (reemplázalo) con los detalles y pasos de reproducción.
Objetivo de respuesta: 72 h.

> Nota: mientras el proyecto esté en fase de scaffolding, **no lo expongas a
> internet** sin completar al menos los puntos 1–4.
