# Arquitectura

## Principio rector

**El agente marca hacia casa.** El clúster gestionado siempre inicia la conexión
saliente hacia el control plane. Nunca al revés. Esto es lo que permite un solo
diseño para on-prem (detrás de NAT/firewall), AWS (VPC) y OCI, sin abrir puertos
de entrada, sin VPN entre entornos y sin red superpuesta que negociar MTU.

## Componentes

### Control plane (`cmd/controlplane`, `internal/controlplane`)
El cerebro self-hosted. Recibe registros y latidos de los agentes, mantiene el
registro de clústeres y expone la topología agregada a la GUI. Hoy el estado es
en memoria (`Store`); la interfaz está pensada para cambiarlo por Postgres sin
tocar el resto.

### Agente (`cmd/agent`, `internal/agent`)
Un binario ligero que corre dentro de cada clúster. Se registra, obtiene un
token y late periódicamente con un `Snapshot` del estado. El `Collector` es hoy
de ejemplo; en fase 1 se cambia por client-go + Hubble.

### GUI (`web/`)
React + React Flow. Hace poll de `/v1/topology` y dibuja el mapa: consola →
control plane ← clústeres (con sus nodos worker). Los colores e iconos siguen el
lenguaje visual del diagrama de arquitectura de referencia.

## Flujo de datos

Dos transportes con la misma semántica, mismo puerto y misma mTLS (el gRPC se
multiplexa con la API REST por content-type; ver `MixedHandler`):

```
  gRPC (preferido, --transport grpc): UN stream bidireccional de larga vida
    Agente ── hello / snapshots / resultados ──> Control plane ──> Store
    Agente <── acciones AL INSTANTE (hub) ────── Control plane <── GUI encola

  HTTP (clásico, --transport http): latido con las órdenes en la respuesta
    Agente.Collect()  ->  POST /v1/agents/{id}/heartbeat  ->  Store
                                                               │
    GUI (poll 5s)     <-  GET /v1/topology                 <──┘
```

Con gRPC las órdenes de la GUI no esperan al siguiente latido: el `hub` del
control plane despierta al stream del clúster en cuanto se encolan (~50ms de
viaje completo vs hasta N segundos). El contrato del sobre está en
`proto/atlas/v1/channel.proto`; los cuerpos siguen siendo el JSON de `pkg/api`
(única fuente de verdad, común a ambos transportes).

## Contrato

Los tipos compartidos están en `pkg/api`. Es la única fuente de verdad del
formato de datos; la GUI los replica en `web/src/api.ts`.

## Decisiones abiertas (para discutir en issues)

- **Aprovisionamiento:** integrar **Cluster API** para crear/unir clústeres en
  los tres entornos con una sola abstracción.
- **Identidad:** mTLS con rotación de certificados en vez del token de sesión.
- **Persistencia y HA:** Postgres + control plane sin estado y multi-réplica.
