# Atlas

> Plataforma open source para orquestar y **visualizar** Kubernetes en on-premises, AWS y OCI desde una sola consola — con un mapa de topología vivo.
>
> **Nombre provisional.** Renómbralo antes de publicar (incluye el path del módulo Go en `go.mod`).

Licencia: **Apache-2.0** · Estado: **scaffolding (fase 1 / MVP)**

---

## Idea en una frase

Cada clúster corre un **agente** que *marca hacia casa* (conexión saliente mTLS) hacia un **control plane** self-hosted. El control plane agrega el estado y lo expone a una **GUI** que lo dibuja como un mapa editable. Así funciona igual detrás de un firewall on-prem, en una VPC de AWS o en OCI — sin abrir puertos de entrada ni VPN entre entornos.

```
  GUI (React)  ──API──►  Control Plane (Go)  ◄──mTLS saliente──  Agente (Go) en cada clúster
   el mapa                registro + topología                    marca hacia casa
```

## Estructura

```
atlas/
├── cmd/
│   ├── controlplane/     # binario del plano de control
│   └── agent/            # binario del agente (corre en cada clúster)
├── internal/
│   ├── controlplane/     # servidor HTTP + store en memoria
│   └── agent/            # bucle de registro/latido + colector
├── pkg/api/              # tipos compartidos (el contrato)
├── web/                  # GUI: React + TypeScript + React Flow
└── docs/                 # arquitectura
```

## Requisitos

- **Go 1.22+** (usa los patrones de ruta con método/comodín del router estándar)
- **Node 20+** para la GUI

## Arranque rápido (3 terminales)

```bash
# 1) Control plane  ->  http://localhost:8080
make run-controlplane

# 2) Un agente de ejemplo (trae un colector de datos ficticios,
#    así ves el mapa vivo SIN un clúster real todavía)
make run-agent
#   añade más entornos:
#   go run ./cmd/agent --name "prod eks"  --provider aws --cluster-id prod-eks
#   go run ./cmd/agent --name "prod oke"  --provider oci --cluster-id prod-oke

# 3) GUI  ->  http://localhost:5173
make web-install
make web-dev
```

Abre http://localhost:5173 y verás cada agente aparecer en el mapa, con su conexión al control plane y sus nodos worker. Apaga un agente (Ctrl-C) y en ~30 s se marca *offline*.

## La API (contrato)

| Método | Ruta | Quién | Para qué |
|---|---|---|---|
| `POST` | `/v1/agents/register` | agente | alta + token de sesión |
| `POST` | `/v1/agents/{id}/heartbeat` | agente | enviar snapshot del clúster |
| `GET`  | `/v1/topology` | GUI | leer la topología agregada |
| `GET`  | `/healthz` | infra | liveness |

Los tipos viven en [`pkg/api/types.go`](pkg/api/types.go). Cámbialos ahí y propaga a Go y a `web/src/api.ts`.

## Lo que es de verdad y lo que es andamio

- **De verdad:** el modelo agente-saliente, el registro con token, los latidos, el store con expiración de offline, la GUI que hace poll y pinta el mapa. Es el esqueleto correcto.
- **Andamio (TODO fase 1+):**
  - `internal/agent/collector.go` devuelve datos **de ejemplo**. Sustitúyelo por un colector real con **client-go** (`rest.InClusterConfig`, listar Nodes/Deployments). Las **conexiones** reales salen de **Hubble** (Cilium), no de la API de K8s.
  - El transporte es HTTP con latidos periódicos. Para tiempo real y comandos control-plane→agente, evolúcialo a **gRPC bidireccional** o WebSocket (manteniendo la conexión saliente).
  - El store es en memoria. Para multi-réplica y persistencia, mételo detrás de **Postgres**.
  - Añadir **mTLS** de verdad (hoy el token es un placeholder de sesión).

## Roadmap

Ver el diagrama de arquitectura interactivo y el roadmap por fases (fase 0: operar k3s a mano → MVP → versionado → multi-entorno → seguridad → release público).

## Contribuir

Es open source (Apache-2.0). Antes de escribir features grandes, abre un issue con la propuesta. `make fmt vet` antes de cada PR.
