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
| Identidad del agente | ⚠️ token de sesión en texto | **mTLS** con certificados por agente y rotación |
| Transporte | ⚠️ HTTP | **TLS** obligatorio (o mTLS) extremo a extremo |
| AuthN/AuthZ de la GUI | ❌ ninguna | OIDC/SSO + RBAC por usuario |
| CORS | `*` por defecto | fija el origen: `--cors-origin https://tu-gui` |
| Límite de tamaño de cuerpo | ✅ 1 MiB | — |
| Timeouts del servidor | ✅ read/write | añadir rate-limiting por agente |
| Contenedores | ✅ distroless, no-root | escaneo de imágenes en CI (Trivy) |
| Secretos | ❌ no hay gestión | integrar con Secret manager / SOPS |

## Cómo endurecerlo (orden recomendado)

1. **TLS ya**: pon el control plane detrás de TLS antes de exponerlo.
2. **mTLS agente↔control plane**: sustituye el token por certificados de
   cliente; el token de hoy es un marcador de posición de sesión, no una
   credencial fuerte.
3. **AuthN de la GUI**: OIDC (p. ej. con tu IdP corporativo) + RBAC.
4. **Fija CORS** al dominio real de la GUI.
5. **Escaneo continuo**: Trivy sobre las imágenes y `govulncheck` sobre el
   código (ya está en CI) en cada PR.

## Reportar una vulnerabilidad

No abras un issue público para vulnerabilidades. Escribe a
`security@TU-DOMINIO` (reemplázalo) con los detalles y pasos de reproducción.
Objetivo de respuesta: 72 h.

> Nota: mientras el proyecto esté en fase de scaffolding, **no lo expongas a
> internet** sin completar al menos los puntos 1–4.
