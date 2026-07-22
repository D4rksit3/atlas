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

## Arranque rápido

### Opción A — con Docker (no necesitas instalar nada)

```bash
docker compose up --build
```

Abre **http://localhost:5173**. Verás los 3 agentes de ejemplo (on-prem / AWS /
OCI) aparecer en el mapa con su conexión al control plane.

### Opción B — nativo, un solo comando

Requisitos: **Go 1.22+** y **Node 20+**.

```bash
./scripts/dev.sh      # o:  make up
```

Compila, elige un puerto libre para el control plane, arranca 3 agentes de
ejemplo y la GUI apuntando al puerto correcto, y limpia todo al salir (Ctrl-C).

> El agente trae un **colector de datos ficticios**, así ves el mapa vivo **sin
> un clúster real todavía**. Apaga un agente y en ~30 s se marca *offline*.

### Manual (3 terminales)

```bash
make run-controlplane          # http://localhost:8080
make run-agent                 # un agente on-prem de ejemplo
make web-install && make web-dev   # http://localhost:5173
```

## Operar el clúster desde la GUI

La GUI ya no es solo lectura: al clicar una carga en el mapa se abre un **Inspector**
para **escalar** réplicas o **reiniciar** (rollout). La orden se encola en el
control plane y viaja de vuelta al agente **en la respuesta del latido** — el
agente la ejecuta con client-go y reporta el resultado. Así "controlar desde el
GUI" no abre ningún puerto en el clúster (el agente sigue marcando hacia casa).

```
GUI ─POST /v1/clusters/{id}/actions─► Control Plane ─(en el latido)─► Agente ─client-go─► clúster
                                            ▲                                     │
                                            └────────── resultado ────────────────┘
```

Verificado E2E (`make test-actions`): escalar y reiniciar cambian el clúster de
verdad y la acción llega a estado `done`.

### Autenticación (login integrado y/u OIDC, con RBAC)

Atlas trae **login propio** (como ArgoCD o Grafana): usuario/contraseña con
bcrypt y sesiones firmadas (HMAC) con caducidad. **El instalador lo activa
siempre** — genera la contraseña del `admin`, la guarda en el Secret
`atlas-auth` y la muestra una sola vez — así la GUI **nunca queda abierta**.

```bash
controlplane --admin-password 'mi-secreto'          # login local
# y/o SSO con tu IdP:
controlplane --oidc-issuer https://TU-IDP --oidc-client-id atlas-gui \
  --rbac-operators "ops@tu-dominio.com,platform-team"
```

Con **OIDC** (Authorization Code + PKCE) el control plane verifica el token
(firma vía JWKS, `iss`/`aud`/`exp`). Ambos métodos conviven: la GUI muestra el
formulario, el botón SSO, o los dos. El **RBAC** es por rol: autenticado =
**viewer** (lee el mapa); operadores (y el admin local) pueden **operar**.
`/v1/login` lleva rate limit estricto por IP y cada intento queda auditado.

Sin ninguno de los dos, la auth queda **deshabilitada** (solo desarrollo). Solo
se protegen los endpoints de la GUI; los del agente ya usan **mTLS**. Verificado
E2E: `make test-login` (401 sin sesión, token manipulado rechazado, 429 de
fuerza bruta) y `make test-oidc` (viewer 403 al operar; operator 202; PKCE
completo en navegador).

### Auditoría

Cada acción deja **rastro de quién la solicitó y su resultado**. El panel
**Actividad** de la GUI (y `GET /v1/audit`) muestra las entradas
(`solicitó`/`ejecutó`, con `ok`/`error`), atribuidas al usuario OIDC. Verificado
con `make test-audit`.

## Vistas del mapa: flujo y por nodo

El mapa tiene dos vistas (toggle en la barra):

- **Flujo**: consola → control plane → clúster → nodos → cargas, con las conexiones
  de Hubble y la ubicación de pods.
- **Por nodo**: cada nodo (servidor) es una caja con las cargas cuyos pods corren
  **en él**, con el conteo por nodo. Hace visible el modelo *un servidor = un nodo*.

**¿Y si agrego otro servidor al clúster?** No hay que configurar nada: el agente
lista los nodos en cada latido, así que un nodo nuevo (p. ej. `kubeadm join` o
`k3d node create`) **aparece solo** en el mapa como una caja más, con sus pods.

## Editar el mapa

Clic en un clúster o carga → el Inspector permite ponerle **alias**, **color** y
**nota** (metadatos que **no tocan el clúster**, solo la presentación). Se guardan
en el control plane (`PUT /v1/annotations/{clave}`) para que **todo el equipo vea
lo mismo**, requieren rol **operator** y quedan **auditadas**. Verificado con
`make test-annotations`.

## Complementos: un catálogo "todo en uno" desde la GUI

En el Inspector de un **clúster**, la sección **Complementos** ofrece un catálogo
por categorías, instalables con un clic (o **instalado ✓** si Atlas los detecta):

| Categoría | Complemento | Instala vía |
|---|---|---|
| GitOps | **Argo CD** | manifiesto |
| Seguridad | **Kyverno** (políticas) · **Falco** (runtime eBPF) | manifiesto · **Helm** |
| Redes | **MetalLB** (LoadBalancer) · **Ingress NGINX** · **cert-manager** | manifiesto · **Helm** · **Helm** |
| Monitoreo | **Metrics Server** · **Prometheus + Grafana** (kube-prometheus-stack) | manifiesto · **Helm** |

Dos formas de instalar: un **manifiesto único** (server-side apply) o un **chart
de Helm** (SDK de Helm compilado en el agente — no necesita el binario `helm`, y
crea un *release* real). La orden viaja por el canal de acciones (auditada).
Añadir un complemento nuevo = una entrada más en el catálogo (`pkg/api` + el
agente). Verificado E2E: `make test-addons` (Kyverno, manifiesto) y `make
test-helm` (Falco, Helm → crea el release y el DaemonSet). El
**kube-prometheus-stack** (Prometheus + Grafana + Alertmanager) usa la misma vía
Helm y arranca completo (todos los componentes Ready).

Algunos complementos exponen **valores editables** al instalar (formulario en la
GUI): p. ej. la **contraseña de Grafana** y la **retención de Prometheus**. El
agente solo aplica valores en **paths de Helm vetados** por el catálogo
(`AddonParam.Path`), nunca rutas arbitrarias. Verificado con `make test-values`.

En un complemento **ya instalado** con parámetros, el botón *editar* reabre el
formulario y hace `helm upgrade` conservando el resto de valores (`ReuseValues`).
Verificado con `make test-upgrade`.

### El panel Servicios: lo instalado, adoptado y accesible

Todo lo que se instala **se adopta en el panel "Servicios"** de la barra del
mapa: cada complemento con interfaz (Grafana, Argo CD…) aparece con su estado y,
si aún no tiene URL, un botón **Publicar** — eliges dominio, clase de ingress y
TLS opcional, y el agente crea el Ingress `atlas-<service>` (acción `expose`,
auditada; el Service debe existir y el host se valida). En cuanto el agente ve
el Ingress, la fila muestra la URL y el botón **Abrir ↗** — con la pista de
credenciales iniciales de cada servicio. Las rutas publicadas **por fuera de
Atlas** también se adoptan: el agente lee todos los Ingress del clúster y el
panel las lista igual. Verificado E2E con `make test-services` (publica un
servicio real en k3d y comprueba que responde por su dominio).

### Publicar servicios con TLS (cert-manager)

Con **cert-manager** instalado, el Inspector del clúster muestra **Publicar con
TLS**: un formulario (email de la cuenta ACME + entorno **staging/production**)
que crea un **ClusterIssuer** de Let's Encrypt (reto HTTP-01 resuelto por el
Ingress). El servidor ACME se **deriva del entorno vetado** — la GUI nunca manda
una URL arbitraria. A partir de ahí, publicar un servicio con HTTPS es anotar su
Ingress con `cert-manager.io/cluster-issuer: letsencrypt-<env>`. Junto a
**MetalLB** (IP) e **Ingress NGINX** (enrutado) cierra la cadena de publicación.
Verificado E2E con `make test-ingress` (instala ambos, crea el ClusterIssuer y
espera a que cert-manager lo registre en ACME staging y lo deje **Ready**).

> Nota (Helm in-cluster): Helm necesita un directorio de caché escribible. Con
> `readOnlyRootFilesystem`, monta un `emptyDir` y define `HELM_CACHE_HOME`,
> `HELM_CONFIG_HOME` y `HELM_DATA_HOME` (ver `deploy/agent-addons.yaml`).

> ⚠️ **Requiere permiso ampliado.** Instalar crea CRDs, ClusterRoles, etc., así
> que el agente necesita permisos ~de admin. Por eso el catálogo es **cerrado**
> (solo complementos vetados, versión fijada — nunca YAML arbitrario) y el RBAC es
> **opt-in**: `kubectl apply -f deploy/agent-addons.yaml`. Decídelo conscientemente.

### Proyectos GitOps: cada repo, en el mapa

Con ArgoCD instalado, el Inspector del clúster tiene una sección **Proyectos
(GitOps)**: lista las Applications con su estado (**Synced/OutOfSync**,
**Healthy/Degraded**) y un formulario **Añadir proyecto** (repo Git, ruta,
namespace destino). Al añadirlo, el agente crea la Application con **auto-sync**,
así que **cada cambio que hagas en el repo aparece solo** en el clúster y en el
mapa (los proyectos se dibujan como nodos GitOps, coloreados por su estado).

Verificado E2E (`make test-gitops`): registrar un proyecto → ArgoCD lo sincroniza
→ Atlas lo muestra **Synced** y sus cargas se despliegan solas.

**Sincronizar y revertir desde el mapa:** clic en un nodo de proyecto GitOps y el
Inspector muestra su estado, el **árbol de recursos** que despliega (de
`status.resources`) y botones **Sincronizar** (fuerza una sync) y **Revertir**
(vuelve a la revisión anterior del historial, pausando el auto-sync para que no
vuelva a avanzar). El agente lo hace tocando la Application (no necesita
cluster-admin: solo permiso sobre `applications`). Verificado con `make test-sync`.

## Vincular otro clúster (asistente) y acceder a Grafana

- **Vincular clúster:** el botón *"+ Vincular clúster"* abre un asistente que, a
  partir del nombre/proveedor/URL, genera **listos para copiar**: el comando del
  certificado (`atlas-certs client`), el `Secret` y el manifiesto del agente con
  mTLS. **No expone la CA** ni muta nada — tú ejecutas los comandos.
- **Acceder a Grafana:** cuando el monitoreo está instalado, el Inspector del
  clúster muestra el comando de `port-forward` y un enlace **Abrir Grafana**.

## Desplegar Atlas dentro de Kubernetes

Corre el control plane y la GUI en un clúster y conéctale agentes (del mismo o de
otros entornos). Guía completa en **[docs/DEPLOY.md](docs/DEPLOY.md)**:

```bash
kubectl apply -f deploy/controlplane.yaml   # control plane + Service
kubectl apply -f deploy/web.yaml            # GUI (nginx) + proxy a la API
kubectl apply -f deploy/agent.yaml          # agente que lee este clúster
```

Verificado E2E (`make test-deploy`): Atlas desplegado en kind termina
**monitoreándose a sí mismo** — ve sus propios pods (`atlas-controlplane`,
`atlas-web`, `atlas-agent`) como cargas, con su ubicación por nodo.

## Observabilidad

El control plane expone, además de la API:

| Endpoint | Para qué |
|---|---|
| `GET /healthz` · `GET /readyz` | liveness / readiness (para K8s o balanceadores) |
| `GET /metrics` | métricas en formato **Prometheus** (peticiones, registros, latidos, clústeres online) |

Registro estructurado de cada petición (método, ruta, latencia) por stdout.

## Seguridad

Lee **[SECURITY.md](SECURITY.md)**: modelo de amenazas, qué ya está bien y qué
falta. El agente y el control plane hablan por **mTLS** (TLS 1.3, certificado
por agente verificado contra la CA de Atlas):

```bash
make certs        # genera CA + certificado de servidor + uno de agente en ./certs
make test-mtls    # verifica E2E: sin cert → rechazado, cert impostor → rechazado, válido → registra
```

Sin certificados, el control plane arranca en HTTP (solo desarrollo). Falta
todavía OIDC/RBAC en la GUI y fijar CORS. El CI incluye `govulncheck`.
**No lo expongas a internet sin completar la lista de SECURITY.md.**

## La API (contrato)

| Método | Ruta | Quién | Para qué |
|---|---|---|---|
| `POST` | `/v1/agents/register` | agente | alta + token de sesión |
| `POST` | `/v1/agents/{id}/heartbeat` | agente | enviar snapshot del clúster |
| `GET`  | `/v1/topology` | GUI | leer la topología agregada |
| `GET`  | `/healthz` | infra | liveness |

Los tipos viven en [`pkg/api/types.go`](pkg/api/types.go). Cámbialos ahí y propaga a Go y a `web/src/api.ts`.

## Conectar un clúster real (colector kube)

El agente tiene dos colectores:

- `sample` (por defecto): datos ficticios, para ver el mapa sin clúster.
- `kube`: lee un clúster **real** con **client-go** (nodos + Deployments/StatefulSets).

```bash
# Local, contra tu k3s (usa ~/.kube/config o $KUBECONFIG):
go run ./cmd/agent --collector kube --name "mi k3s" --provider onprem \
  --control-plane http://localhost:8080

# Dentro de un clúster (in-cluster, RBAC de solo lectura):
kubectl apply -f deploy/agent.yaml   # ajusta imagen y ATLAS_CONTROL_PLANE antes
```

El colector kube usa `rest.InClusterConfig()` si corre como Pod, o el kubeconfig
si corre fuera. Los permisos son **mínimos** (solo lectura de nodos y cargas) —
ver `deploy/agent.yaml`.

### Probarlo de verdad en 1 comando

Si tienes **docker + kind + kubectl**, este script levanta un clúster real de 3
nodos, despliega cargas, corre el agente en modo kube y **verifica que el mapa
coincide con el clúster** (incluida una prueba de mapa vivo escalando un
Deployment). Limpia todo al terminar:

```bash
make test-kube        # o:  ./scripts/test-kube.sh
```

> Verificado E2E contra Kubernetes 1.30 (kind): 3 nodos con sus roles, cargas
> reales de todos los namespaces, y actualización en vivo al escalar réplicas.

## Conexiones reales entre servicios (Hubble)

La API de Kubernetes **no sabe quién habla con quién**. Esas conexiones (los
`links` del mapa) salen de **Hubble**, la observabilidad de red de **Cilium**.
El agente las obtiene con `--links hubble`:

```bash
# Requiere Cilium + Hubble Relay en el clúster. Fuera del clúster, port-forward:
kubectl -n kube-system port-forward svc/hubble-relay 4245:80 &
go run ./cmd/agent --collector kube --links hubble --hubble-server localhost:4245 \
  --name "mi k3s" --provider onprem --control-plane http://localhost:8080

# In-cluster: el relay suele estar en hubble-relay.kube-system:80 (valor por defecto).
```

El colector muestrea los últimos flujos, se queda solo con el tráfico
**iniciado** (no las respuestas) y lo agrega en enlaces dirigidos
`origen → destino` entre cargas. Verificado E2E: `web → api`, `web → db`,
`web → coredns` aparecen en el mapa a partir del tráfico real observado por
Cilium. Reprodúcelo con **`make test-hubble`** (levanta kind + Cilium + Hubble).

## Lo que es de verdad y lo que es andamio

- **De verdad:** el modelo agente-saliente, el registro con token, los latidos, el store con expiración de offline, la GUI que hace poll y pinta el mapa, y el **colector kube con client-go** (verificado E2E contra un clúster kind real — `make test-kube`). Es el esqueleto correcto.
- **De verdad (fase 2):** el **colector Hubble** (`--links hubble`, `make test-hubble`); la **ubicación de pods** por nodo (`make test-kube`); el **despliegue in-cluster** (`make test-deploy`); el **mTLS** agente↔control plane (`make test-mtls`); el **store Postgres** persistente y multi-réplica (`--store postgres`, `make test-postgres`); **operar cargas desde la GUI** — escalar/reiniciar vía el canal de órdenes (`make test-actions`); y la **auth de la GUI** — OIDC (PKCE) + RBAC viewer/operator (`make test-oidc`).
- **Andamio (TODO fase 2+):**
  - El transporte es HTTP con latidos periódicos. Para tiempo real y comandos control-plane→agente, evolúcialo a **gRPC bidireccional** o WebSocket (manteniendo la conexión saliente).
  - Seguridad ya cubierta: **OIDC/RBAC en la GUI** (`make test-oidc`), **rotación** (hojas cortas + hot-reload, `make test-rotation`) y **revocación inmediata por CRL** (`atlas-certs revoke` + `--tls-crl`, `make test-revocation`). Queda **OCSP** y acotar el egress de la NetworkPolicy.

## Roadmap

Ver el diagrama de arquitectura interactivo y el roadmap por fases (fase 0: operar k3s a mano → MVP → versionado → multi-entorno → seguridad → release público).

## Contribuir

Es open source (Apache-2.0). Antes de escribir features grandes, abre un issue con la propuesta. `make fmt vet` antes de cada PR.
