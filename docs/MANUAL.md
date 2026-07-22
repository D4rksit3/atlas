# Manual de Atlas — instalar desde cero y usar

Guía completa para poner Atlas en marcha **partiendo de un servidor limpio** y
después usarlo día a día. Atlas es una consola open source que automatiza y
**visualiza Kubernetes como un mapa vivo editable**: instalas complementos,
publicas y administras servicios, gestionas nodos, usuarios y alertas — todo
desde la GUI, sin `kubectl`.

Hay tres formas de arrancar, de menos a más "de verdad":

| | Para qué | Necesitas | Tiempo |
|---|---|---|---|
| **A. Demo con Docker** | ver la GUI con datos de ejemplo (sin Kubernetes) | Docker | 2 min |
| **B. Local nativo** | desarrollar / trastear el código | Go 1.22+, Node 20+ | 3 min |
| **C. Clúster real (k3d)** | usarlo contra un Kubernetes de verdad | Docker, k3d, kubectl | 10 min |
| **D. Producción** | exponerlo en tu dominio | un clúster + DNS | 20 min |

Si es tu primera vez, haz la **A** para ver qué es, y luego salta a la **C**.

---

## 0. Requisitos según la opción

Instala solo lo que pida la opción que vayas a usar.

```bash
# Docker (todas las opciones lo agradecen)
curl -fsSL https://get.docker.com | sh

# k3d (Kubernetes ligero sobre Docker) + kubectl  — opciones C y D
curl -s https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash
curl -LO "https://dl.k8s.io/release/$(curl -Ls https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
sudo install kubectl /usr/local/bin/kubectl

# Go 1.22+ y Node 20+  — solo opción B (desarrollo)
# https://go.dev/dl   y   https://nodejs.org
```

Clona el repositorio (todas las opciones parten de aquí):

```bash
git clone https://github.com/D4rksit3/atlas.git
cd atlas
```

---

## A. Demo con Docker (sin Kubernetes)

Levanta el control plane, la GUI y **tres agentes de ejemplo** (on-prem, AWS,
OCI) con datos ficticios. Ideal para ver el mapa en 2 minutos.

```bash
docker compose up --build
```

Abre **http://localhost:5173**. Verás tres clústeres de ejemplo en el mapa, con
sus nodos, cargas, servicios e IPs. No hay login (es una demo). Para parar:
`Ctrl-C` y `docker compose down`.

---

## B. Local nativo (desarrollo)

Un solo comando compila los binarios y arranca todo (control plane + 3 agentes
de ejemplo + GUI en modo desarrollo con recarga en caliente):

```bash
make up          # = ./scripts/dev.sh
```

Abre **http://localhost:5173**. Al salir con `Ctrl-C` limpia todos los procesos.

Otros atajos útiles:

```bash
make build              # compila bin/controlplane y bin/agent
make test               # tests de Go con -race
make web-dev            # solo la GUI (Vite :5173)
make help               # lista todos los atajos
```

---

## C. Clúster real con k3d (lo recomendado para probarlo de verdad)

Aquí Atlas gobierna un Kubernetes real. El **instalador** (`scripts/install.sh`)
te pregunta el dominio y si es local o público, y deja todo coherente.

### C.1 Crea un clúster k3d

```bash
# Un clúster con el ingress (traefik) expuesto en el puerto 8088 de tu máquina
k3d cluster create atlas --agents 1 -p "8088:80@loadbalancer" --wait
kubectl get nodes            # deberías ver los nodos Ready
```

> Si el nodo se queda en `NotReady` mucho rato, sube los límites de inotify del
> host (el kubelet los necesita):
> `sudo sysctl -w fs.inotify.max_user_instances=1024 fs.inotify.max_user_watches=1048576`

### C.2 Construye las imágenes e impórtalas al clúster

Sin registro externo: se construyen local y se importan directamente.

```bash
docker build -t atlas-controlplane:dev --target controlplane .
docker build -t atlas-agent:dev        --target agent .
docker build -t atlas-web:dev -f web/Dockerfile .
k3d image import -c atlas atlas-controlplane:dev atlas-agent:dev atlas-web:dev
```

### C.3 Instala Atlas

El instalador crea el login (usuario `admin` + contraseña generada), aplica las
NetworkPolicies y despliega control plane, GUI y un agente que monitorea este
mismo clúster.

```bash
ATLAS_IMAGE_PREFIX="" ATLAS_TAG=dev \
  ./scripts/install.sh --domain atlas.local --mode local
```

Al terminar imprime **UNA sola vez** las credenciales:

```
   ┌─ Credenciales de la GUI (guárdalas AHORA; no se vuelven a mostrar) ─
   │  usuario:    admin
   │  contraseña: 63a8563416f74a70bab5
   └─ Para cambiarla: kubectl -n atlas-system delete secret atlas-auth && ./scripts/install.sh
```

Guárdalas. Espera a que arranque:

```bash
kubectl -n atlas-system rollout status deploy/atlas-controlplane
kubectl -n atlas-system rollout status deploy/atlas-web
```

### C.4 Entra

Como usamos el dominio de ejemplo `atlas.local`, resuélvelo a tu máquina:

```bash
echo "127.0.0.1 atlas.local" | sudo tee -a /etc/hosts
```

Abre **http://atlas.local:8088**, inicia sesión con `admin` + la contraseña
generada, y ya estás dentro.

> El instalador acepta flags: `--domain`, `--mode local|public`,
> `--image-prefix`, `--tag`, `--ingress-class`, `--oidc-issuer`,
> `--oidc-client-id`, `--operators`, `--no-agent`, `--render-only` (imprime los
> manifiestos sin aplicarlos). Sin `--domain`/`--mode` te los pregunta.

---

## D. Producción (tu dominio)

Dos escenarios habituales.

### D.1 Con Ingress + cert-manager (TLS automático)

Requisitos: un clúster con Ingress Controller (nginx), cert-manager con un
`ClusterIssuer` (p. ej. `letsencrypt-prod`), y el DNS de tu dominio apuntando a
la IP del Ingress.

```bash
# Publica las imágenes en tu registro (GitHub Container Registry, Harbor…)
docker build -t ghcr.io/d4rksit3/atlas-controlplane:v1 --target controlplane .
docker build -t ghcr.io/d4rksit3/atlas-agent:v1        --target agent .
docker build -t ghcr.io/d4rksit3/atlas-web:v1 -f web/Dockerfile .
docker push ghcr.io/d4rksit3/atlas-{controlplane,agent,web}:v1

# Instala en modo público (https + cert-manager + ingress nginx)
./scripts/install.sh --domain atlas.tudominio.com --mode public \
  --image-prefix ghcr.io/d4rksit3 --tag v1
```

Espera el certificado y abre `https://atlas.tudominio.com`:

```bash
kubectl -n atlas-system get certificate atlas-web-tls -w
```

### D.2 Detrás de un proxy que ya termina TLS (p. ej. Nginx Proxy Manager)

Si el servidor ya tiene un proxy en 80/443 para otros dominios, no compitas por
esos puertos: que ESE proxy termine el TLS.

```bash
# k3d con el ingress en un puerto interno libre
k3d cluster create atlas --agents 1 -p "8880:80@loadbalancer" --wait

# imágenes :prod importadas al clúster (como en C.2, con tag prod)
# instala en modo local y ajusta CORS al origen https público:
ATLAS_IMAGE_PREFIX="" ATLAS_TAG=prod \
  ./scripts/install.sh --domain atlas.tudominio.com --mode local
kubectl -n atlas-system set env deploy/atlas-controlplane \
  ATLAS_CORS_ORIGIN=https://atlas.tudominio.com
```

En el proxy externo: un host para `atlas.tudominio.com` → `IP-privada:8880`
(HTTP) con su certificado Let's Encrypt y **Force SSL**. El login local de Atlas
ya protege la consola aunque no configures OIDC.

Guía ampliada de producción: [`docs/PUBLISH.md`](PUBLISH.md).

---

## Primer arranque: qué configurar

1. **Cambia la contraseña del admin** (o créate tu usuario) — ver *Usuarios* abajo.
2. Si vas a instalar complementos (ArgoCD, Grafana…), aplica el RBAC ampliado
   **opt-in** una vez:
   ```bash
   kubectl apply -f deploy/agent-addons.yaml
   ```
   (Instalar crea CRDs y roles, por eso el agente necesita permisos de admin;
   el catálogo es cerrado y con versiones fijadas — nunca YAML arbitrario.)

---

# Cómo se usa

La barra superior del mapa tiene: el **selector de vista** (Flujo · Por nodo ·
Red) y los botones **Alertas · Servicios · Usuarios · Actividad**. Clicar
cualquier nodo del mapa abre el **Inspector** a la derecha.

## El mapa y sus tres vistas

- **Flujo** — la topología por capas: consola → control plane → clústeres →
  nodos → cargas. Las flechas mTLS muestran a cada agente "marcando hacia casa".
- **Por nodo** — cada servidor es una caja con las cargas cuyos pods corren en
  él. Clic en la cabecera para gestionar el nodo.
- **Red** — cómo se comunican los pods, **ordenado por namespace**:
  `host publicado → Service (ClusterIP:puerto) → cargas con las IPs de sus pods`.
  Las cargas sin Service delante se agrupan aparte.

La vista elegida se recuerda entre sesiones. Consumo de **CPU/memoria en vivo**
aparece en los nodos y cargas si el clúster tiene `metrics-server` (instálalo
desde el catálogo).

## Inspector: operar y diagnosticar una carga

Clic en una carga (Deployment/StatefulSet) → el Inspector muestra:

- **Editar** — alias, color y nota (metadatos que NO tocan el clúster).
- **Pods e IPs** — cada pod con su IP, nodo y estado.
- **Diagnóstico** — **Ver logs** (cola de los pods) y **Eventos del namespace**,
  sin salir de Atlas.
- **Operar** — escalar (±réplicas) y reiniciar (rollout).

## El módulo Servicios: instalar y ADMINISTRAR todo

Botón **Servicios**. Por cada clúster:

- **Catálogo** — despliega ArgoCD, Kyverno, Falco, MetalLB, Ingress NGINX,
  cert-manager, Metrics Server, Prometheus + Grafana… Los que tienen valores
  editables (p. ej. la contraseña de Grafana) muestran un formulario.
- **Administrar** — abre el servicio DENTRO de Atlas: su interfaz **embebida**
  (Grafana, ArgoCD) en el centro y, a la izquierda, sus cargas (escalar /
  reiniciar), la **configuración** (valores de Helm → `helm upgrade`), la
  **publicación** y las credenciales iniciales.
- **Publicar / Despublicar** — crea o retira el Ingress `atlas-<servicio>` con
  dominio, clase y TLS opcional. En cuanto existe la URL aparece **Abrir ↗**.
- **Desinstalar** — quita el complemento (con confirmación); nunca toca los
  namespaces compartidos del sistema.

> Para embeber Grafana en producción, publícalo con **TLS** (una página https no
> puede embeber contenido http). Grafana se instala ya con `allow_embedding`.

## Publicar un servicio con HTTPS (cert-manager)

En el Inspector del clúster, con cert-manager instalado, **Publicar con TLS**
crea un `ClusterIssuer` de Let's Encrypt (elige email y staging/production).
Después, publicar cualquier servicio con TLS es un clic en el módulo Servicios.

## Proyectos GitOps (ArgoCD)

Con ArgoCD instalado, el Inspector del clúster te deja **añadir un proyecto**
(repo Git + ruta + namespace). El mapa muestra cada proyecto con su estado
sync/health y su árbol de recursos; puedes **Sincronizar** o **Revertir**. Cada
push a tu repo se refleja solo.

## Gestión de nodos

Clic en un nodo → **Gestionar nodo**:

- **Acordonar** (cordon) — deja de aceptar pods nuevos.
- **Vaciar** (drain) — acordona y desaloja sus pods respetando los
  PodDisruptionBudgets (los DaemonSets se quedan). Para mantenimiento.
- **Reabrir** (uncordon).

## Namespaces con cuotas

En el Inspector del clúster, **Crear namespace**: nombre + límites opcionales de
CPU/memoria (crea una `ResourceQuota`). Para ordenar el clúster por equipos.

## Usuarios y roles

Botón **Usuarios** (requiere rol *operator*). Crea usuarios locales para tu
equipo sin compartir la contraseña del admin:

- **viewer** — solo lee el mapa.
- **operator** — puede operar (escalar, instalar, publicar, gestionar…).

Se guardan con bcrypt y cada alta/baja queda auditada. Para **cambiar la
contraseña del admin**:

```bash
kubectl -n atlas-system delete secret atlas-auth
kubectl -n atlas-system create secret generic atlas-auth \
  --from-literal=adminUser=admin \
  --from-literal=adminPassword='TU-NUEVA-CONTRASEÑA' \
  --from-literal=sessionKey="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
kubectl -n atlas-system rollout restart deploy/atlas-controlplane
```

## Alertas

Botón **Alertas** (contador naranja/rojo cuando hay algo). Atlas vigila:
clúster offline, nodos NotReady, pods en CrashLoop o sin imagen. Para recibir
avisos **fuera de la GUI**, arranca el control plane con un webhook:

```bash
kubectl -n atlas-system set env deploy/atlas-controlplane \
  ATLAS_ALERT_WEBHOOK=https://tu-webhook   # n8n, proxy de Slack/Discord/Telegram…
```

Recibe un POST JSON al **aparecer** y al **resolverse** cada alerta.

## Actividad (auditoría)

Botón **Actividad**: quién hizo qué y cuándo (instalar, escalar, publicar,
login, gestión de usuarios…), con el resultado de cada acción.

## Vincular OTROS clústeres

Botón **+ Vincular clúster** (arriba). Con la CA montada en el control plane
(ver [`docs/PUBLISH.md`](PUBLISH.md)), genera un **token de un solo uso** (15
min): en el clúster nuevo ejecutas UN comando y el agente aparece solo en el
mapa.

```bash
curl -sf https://atlas.tudominio.com/v1/enroll/TOKEN | kubectl apply -f -
```

Sin la CA montada, usa el flujo manual (colapsable en el mismo asistente).

---

## Operaciones y resolución de problemas

```bash
# Estado de los pods de Atlas
kubectl -n atlas-system get pods

# Logs del control plane / agente
kubectl -n atlas-system logs deploy/atlas-controlplane
kubectl -n atlas-system logs deploy/atlas-agent

# ¿La GUI responde? (ajusta puerto/dominio)
curl -I --resolve atlas.local:8088:127.0.0.1 http://atlas.local:8088/
```

| Síntoma | Causa probable | Solución |
|---|---|---|
| "usuario o contraseña incorrectos" | contraseña mal / rate limit | usa la del instalador; si probaste mucho, espera ~30 s |
| GUI carga pero mapa vacío | el agente aún no registra | espera unos segundos; mira `logs deploy/atlas-agent` |
| Instalar complemento falla con RBAC | falta el opt-in | `kubectl apply -f deploy/agent-addons.yaml` |
| Agente OOMKilled al instalar Grafana | chart grande | ya viene con límite 512Mi; no lo bajes de 256Mi |
| Nodo k3d en NotReady al crear el clúster | límites inotify | sube `fs.inotify.max_user_*` (ver C.1) |
| Grafana no se embebe en Administrar | contenido mixto | publícalo con TLS (Atlas https + servicio http se bloquea) |

## Desinstalar Atlas

```bash
kubectl delete namespace atlas-system
kubectl delete -f deploy/networkpolicy.yaml --ignore-not-found
# y si fue un k3d de prueba:
k3d cluster delete atlas
```

---

## Verificarlo end-to-end

Cada capacidad tiene su prueba automática (`make test-*`, listadas con
`make help`). Las más representativas:

```bash
make test-login       # el login local cierra la API y audita (sin clúster)
make test-services    # k3d real: publicar, red con IPs, logs, nodos, cuotas… (21 checks)
make test-enroll      # vinculación por token (cert al vuelo firmado por la CA)
```

## Dónde seguir

- **[README.md](../README.md)** — visión general y detalles de cada capacidad.
- **[docs/PUBLISH.md](PUBLISH.md)** — producción, proxy externo y activar la
  vinculación por token.
- **[SECURITY.md](../SECURITY.md)** — modelo de seguridad y estado de cada
  control.
