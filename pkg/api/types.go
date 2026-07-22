// Package api define los tipos que comparten el control plane, el agente y la
// GUI. Es el contrato de la plataforma: si cambias algo aquí, cambia en las
// tres capas a la vez.
package api

import "time"

// Provider identifica el entorno donde vive un clúster.
type Provider string

const (
	ProviderOnPrem Provider = "onprem"
	ProviderAWS    Provider = "aws"
	ProviderOCI    Provider = "oci"
)

// ---- Peticiones agente -> control plane (el agente siempre inicia) ----

// RegisterRequest lo envía el agente al arrancar para darse de alta.
type RegisterRequest struct {
	ClusterID    string   `json:"clusterId"`
	Name         string   `json:"name"`
	Provider     Provider `json:"provider"`
	AgentVersion string   `json:"agentVersion"`
}

// RegisterResponse le dice al agente su token de sesión y cada cuánto latir.
type RegisterResponse struct {
	Token                    string `json:"token"`
	HeartbeatIntervalSeconds int    `json:"heartbeatIntervalSeconds"`
}

// Heartbeat lleva un snapshot del estado del clúster en cada latido, y de paso
// reporta el resultado de las acciones que el agente ejecutó desde el anterior.
type Heartbeat struct {
	Token    string         `json:"token"`
	Snapshot Snapshot       `json:"snapshot"`
	Results  []ActionResult `json:"results,omitempty"`
}

// HeartbeatResponse es lo que el control plane devuelve en cada latido: las
// acciones pendientes que el agente debe ejecutar. Así las órdenes viajan de
// vuelta por la MISMA conexión saliente — sin abrir puertos en el clúster.
type HeartbeatResponse struct {
	Actions []Action `json:"actions,omitempty"`
}

// ---- Modelo de topología ----

// Node es un nodo (servidor) del clúster.
type Node struct {
	Name  string `json:"name"`
	Role  string `json:"role"` // "control-plane" | "worker"
	Ready bool   `json:"ready"`
	// Unschedulable: el nodo está acordonado (cordon) y no acepta pods nuevos.
	Unschedulable bool `json:"unschedulable,omitempty"`
	// Usage: CPU/memoria en uso del nodo, si hay metrics-server.
	Usage *Usage `json:"usage,omitempty"`
}

// Placement dice cuántos pods de una carga corren en un nodo concreto. Una carga
// puede repartir sus réplicas entre varios nodos, así que lleva una lista.
type Placement struct {
	Node string `json:"node"`
	Pods int    `json:"pods"`
}

// Usage es consumo VIVO de recursos (fuente: metrics-server). Presente solo si
// el clúster tiene la API metrics.k8s.io.
type Usage struct {
	CPUm  int64 `json:"cpum"`  // millicores en uso
	MemMi int64 `json:"memMi"` // MiB en uso
}

// PodInfo es un pod concreto de una carga: su IP real, dónde corre y su fase.
// Con esto la GUI enseña "quién es quién" en la red del clúster.
type PodInfo struct {
	Name  string `json:"name"`
	IP    string `json:"ip,omitempty"`
	Node  string `json:"node,omitempty"`
	Phase string `json:"phase,omitempty"` // Running | Pending | ...
}

// Workload es una carga desplegada (Deployment, StatefulSet, ...).
type Workload struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Replicas  int    `json:"replicas"`
	// Placement: en qué nodos corren sus pods y cuántos en cada uno. Vacío si el
	// colector no pudo leer pods (p. ej. sin permiso) o si la carga no tiene pods.
	Placement []Placement `json:"placement,omitempty"`
	// Pods: los pods de la carga con su IP (acotado a MaxPodsPerWorkload para no
	// inflar el snapshot en cargas grandes).
	Pods []PodInfo `json:"pods,omitempty"`
	// Usage: CPU/memoria en uso (suma de sus pods), si hay metrics-server.
	Usage *Usage `json:"usage,omitempty"`
}

// MaxPodsPerWorkload acota cuántos pods (con IP) viajan por carga en el snapshot.
const MaxPodsPerWorkload = 15

// ServicePort es un puerto expuesto por un Service.
type ServicePort struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol,omitempty"` // TCP | UDP
}

// ServiceInfo es un Service del clúster: el "cable" por el que se comunican los
// pods. La GUI lo usa para dibujar la red ordenada: Ingress -> Service -> cargas.
type ServiceInfo struct {
	Name      string        `json:"name"`
	Namespace string        `json:"namespace"`
	Type      string        `json:"type"`                // ClusterIP | NodePort | LoadBalancer | Headless
	ClusterIP string        `json:"clusterIP,omitempty"` // vacío si headless
	Ports     []ServicePort `json:"ports,omitempty"`
	// Workloads: cargas cuyos pods selecciona este Service (a quién enruta).
	Workloads []string `json:"workloads,omitempty"`
}

// Link es una conexión observada entre dos cargas (fuente de datos: Hubble).
type Link struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// AppResource es un recurso que gestiona una Application (parte de su árbol).
type AppResource struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Status    string `json:"status,omitempty"` // Synced | OutOfSync ...
	Health    string `json:"health,omitempty"` // Healthy | Progressing ...
}

// App es una Application de ArgoCD (un "proyecto" GitOps): un repo Git que ArgoCD
// mantiene sincronizado en el clúster. Su estado (sync/health) sale del CRD.
type App struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"` // donde vive la Application (normalmente argocd)
	RepoURL   string `json:"repoURL"`
	Path      string `json:"path"`
	Revision  string `json:"revision,omitempty"`
	Sync      string `json:"sync"`   // Synced | OutOfSync | Unknown
	Health    string `json:"health"` // Healthy | Progressing | Degraded | Missing | ...
	// Resources: el árbol de recursos que despliega (de status.resources).
	Resources []AppResource `json:"resources,omitempty"`
}

// IngressInfo es una ruta de entrada publicada en el clúster: qué host lleva a
// qué Service. Con esto el panel de servicios sabe la URL real de cada cosa.
type IngressInfo struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Class     string `json:"class,omitempty"`
	Host      string `json:"host"`
	Path      string `json:"path,omitempty"`
	Service   string `json:"service"` // Service de backend
	Port      int    `json:"port"`
	TLS       bool   `json:"tls"` // el host tiene bloque TLS (https)
}

// Snapshot es la foto del clúster que el agente envía en cada heartbeat.
type Snapshot struct {
	Nodes     []Node     `json:"nodes"`
	Workloads []Workload `json:"workloads"`
	Links     []Link     `json:"links"`
	// Apps: proyectos GitOps (Applications de ArgoCD) si ArgoCD está instalado.
	Apps []App `json:"apps,omitempty"`
	// Ingresses: rutas publicadas (host -> service), para el panel de servicios.
	Ingresses []IngressInfo `json:"ingresses,omitempty"`
	// Services: los Services del clúster (ClusterIP, puertos, a qué cargas
	// enrutan), para la vista de red.
	Services []ServiceInfo `json:"services,omitempty"`
}

// ---- Acciones: la GUI ordena, el agente ejecuta ----

// Estados de una acción a lo largo de su vida.
const (
	ActionPending    = "pending"    // encolada por la GUI, aún no entregada
	ActionDispatched = "dispatched" // entregada al agente en un latido
	ActionDone       = "done"       // el agente la ejecutó con éxito
	ActionError      = "error"      // el agente falló al ejecutarla
)

// Tipos de acción soportados.
const (
	ActionScale    = "scale"    // cambiar el nº de réplicas de una carga
	ActionRestart  = "restart"  // reinicio suave (rollout) de una carga
	ActionInstall  = "install"  // instalar un complemento vetado (p. ej. ArgoCD)
	ActionAddApp   = "addapp"   // registrar un proyecto GitOps (Application de ArgoCD)
	ActionSync     = "sync"     // forzar sincronización de un proyecto GitOps
	ActionRollback = "rollback" // revertir un proyecto a su revisión anterior
	ActionIssuer   = "issuer"   // crear un ClusterIssuer de cert-manager (TLS ACME)
	ActionExpose   = "expose"   // publicar un servicio: crear su Ingress (host -> service)
	ActionUninstall = "uninstall" // quitar un complemento del catálogo (helm uninstall / borrar manifiesto)
	ActionUnexpose  = "unexpose"  // retirar una publicación (borrar el Ingress atlas-<service>)
	ActionLogs      = "logs"      // diagnóstico: últimos logs de los pods de una carga
	ActionEvents    = "events"    // diagnóstico: eventos recientes de un namespace
	ActionCordon    = "cordon"    // nodo: no aceptar pods nuevos
	ActionUncordon  = "uncordon"  // nodo: volver a aceptar pods
	ActionDrain     = "drain"     // nodo: cordon + desalojar sus pods (mantenimiento)
	ActionCreateNS  = "createns"  // crear un namespace (con cuotas de CPU/memoria opcionales)
)

// NamespaceSpec describe un namespace a crear desde la GUI, con cuotas
// opcionales (ResourceQuota) para ordenar el clúster por equipos/proyectos.
type NamespaceSpec struct {
	Name   string `json:"name"`
	CPU    string `json:"cpu,omitempty"`    // p. ej. "2" (límite total de CPU)
	Memory string `json:"memory,omitempty"` // p. ej. "4Gi" (límite total de memoria)
}

// ExposeSpec describe cómo publicar un servicio del clúster: el agente crea un
// Ingress "atlas-<service>" que enruta el host al Service indicado. Con TLS,
// se anota con el ClusterIssuer para que cert-manager emita el certificado.
type ExposeSpec struct {
	Namespace    string `json:"namespace"`              // dónde vive el Service
	Service      string `json:"service"`                // Service a publicar
	Port         int    `json:"port"`                   // puerto del Service
	Host         string `json:"host"`                   // dominio de entrada
	IngressClass string `json:"ingressClass,omitempty"` // default: nginx
	TLS          bool   `json:"tls,omitempty"`          // https con cert-manager
	Issuer       string `json:"issuer,omitempty"`       // ClusterIssuer (default letsencrypt-production)
}

// IngressClassOr devuelve la clase de Ingress a usar (default "nginx").
func (s ExposeSpec) IngressClassOr() string {
	if s.IngressClass != "" {
		return s.IngressClass
	}
	return "nginx"
}

// IssuerOr devuelve el ClusterIssuer a usar con TLS (default letsencrypt-production).
func (s ExposeSpec) IssuerOr() string {
	if s.Issuer != "" {
		return s.Issuer
	}
	return "letsencrypt-production"
}

// Entornos ACME válidos para un ClusterIssuer. El servidor ACME se DERIVA del
// entorno (nunca es una URL arbitraria de la GUI): así el catálogo de emisores es
// cerrado, como el de complementos.
const (
	ACMEStaging    = "staging"    // Let's Encrypt staging (pruebas, sin límites duros)
	ACMEProduction = "production" // Let's Encrypt producción (certs de verdad)
)

// ACMEDirectory devuelve la URL del directorio ACME para un entorno vetado.
// El segundo valor es false si el entorno no está soportado.
func ACMEDirectory(env string) (string, bool) {
	switch env {
	case ACMEStaging:
		return "https://acme-staging-v02.api.letsencrypt.org/directory", true
	case ACMEProduction:
		return "https://acme-v02.api.letsencrypt.org/directory", true
	default:
		return "", false
	}
}

// IssuerSpec describe el ClusterIssuer de cert-manager a crear: emisor ACME
// (Let's Encrypt) con reto HTTP-01 resuelto por el Ingress. Es lo que convierte a
// cert-manager en algo útil: a partir de aquí, publicar un servicio con TLS es
// añadir una anotación al Ingress. NO expone la URL ACME (se deriva del entorno).
type IssuerSpec struct {
	Name         string `json:"name,omitempty"`         // nombre del ClusterIssuer (default: letsencrypt-<env>)
	Email        string `json:"email"`                  // email de la cuenta ACME (avisos de expiración)
	Environment  string `json:"environment"`            // staging | production
	IngressClass string `json:"ingressClass,omitempty"` // clase de Ingress para el reto HTTP-01 (default: nginx)
}

// IssuerName es el nombre efectivo del ClusterIssuer (el dado, o letsencrypt-<env>).
func (s IssuerSpec) IssuerName() string {
	if s.Name != "" {
		return s.Name
	}
	return "letsencrypt-" + s.Environment
}

// IngressClassOr devuelve la clase de Ingress a usar (default "nginx").
func (s IssuerSpec) IngressClassOr() string {
	if s.IngressClass != "" {
		return s.IngressClass
	}
	return "nginx"
}

// AddonParam es un valor editable de un complemento al instalarlo (p. ej. la
// contraseña de Grafana). Path es la ruta VETADA en los values de Helm; el agente
// solo acepta estos paths, nunca rutas arbitrarias de la GUI.
type AddonParam struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Type    string `json:"type"`    // string | password | int | bool
	Default string `json:"default"` // valor por defecto (texto)
	Path    string `json:"path"`    // ruta en los values de Helm (p. ej. grafana.adminPassword)
}

// AddonAccess describe cómo llegar a la interfaz de un complemento instalado:
// el Service que la sirve y su puerto. El panel de servicios lo usa para
// PUBLICARLA (crear un Ingress) y abrirla desde Atlas.
type AddonAccess struct {
	Service string `json:"service"` // Service de la UI (en el ns del complemento)
	Port    int    `json:"port"`
	Hint    string `json:"hint,omitempty"` // cómo entrar (usuario/contraseña inicial)
}

// AddonInfo describe un complemento del catálogo (metadatos para la GUI y la
// detección de "instalado"). Las URLs de manifiesto viven en el agente.
type AddonInfo struct {
	Key            string       `json:"key"`
	Name           string       `json:"name"`
	Category       string       `json:"category"` // gitops | monitoreo | seguridad | redes
	Description    string       `json:"description"`
	Namespace      string       `json:"namespace"`
	DetectWorkload string       `json:"detectWorkload"`   // carga cuya presencia indica instalado
	Params         []AddonParam `json:"params,omitempty"` // valores editables al instalar
	Access         *AddonAccess `json:"access,omitempty"` // UI del complemento (si tiene)
}

// AddonParams devuelve los parámetros editables de un complemento (o nil).
func AddonParams(key string) []AddonParam {
	for _, a := range Addons() {
		if a.Key == key {
			return a.Params
		}
	}
	return nil
}

// Addons es el catálogo de complementos instalables desde la GUI. Cerrado y
// versionado: el agente solo instala estos (nunca YAML arbitrario).
func Addons() []AddonInfo {
	return []AddonInfo{
		{Key: "argocd", Name: "Argo CD", Category: "gitops", Namespace: "argocd",
			DetectWorkload: "argocd-server", Description: "Despliegue continuo (GitOps)",
			Access: &AddonAccess{Service: "argocd-server", Port: 80,
				Hint: "usuario admin; contraseña inicial: kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d"}},
		{Key: "kyverno", Name: "Kyverno", Category: "seguridad", Namespace: "kyverno",
			DetectWorkload: "kyverno-admission-controller", Description: "Políticas de admisión y seguridad"},
		{Key: "falco", Name: "Falco", Category: "seguridad", Namespace: "falco",
			DetectWorkload: "falco", Description: "Detección de amenazas en runtime (eBPF)"},
		{Key: "metallb", Name: "MetalLB", Category: "redes", Namespace: "metallb-system",
			DetectWorkload: "controller", Description: "LoadBalancer para bare-metal/on-prem"},
		{Key: "ingress-nginx", Name: "Ingress NGINX", Category: "redes", Namespace: "ingress-nginx",
			DetectWorkload: "ingress-nginx-controller", Description: "Publicar servicios HTTP(S): reverse proxy / Ingress"},
		{Key: "cert-manager", Name: "cert-manager", Category: "redes", Namespace: "cert-manager",
			DetectWorkload: "cert-manager", Description: "TLS automático (Let's Encrypt) para los servicios publicados"},
		{Key: "metrics-server", Name: "Metrics Server", Category: "monitoreo", Namespace: "kube-system",
			DetectWorkload: "metrics-server", Description: "Métricas de CPU/memoria (base de monitoreo)"},
		{Key: "kube-prometheus-stack", Name: "Prometheus + Grafana", Category: "monitoreo", Namespace: "monitoring",
			DetectWorkload: "grafana", Description: "Monitoreo completo: Prometheus, Grafana y Alertmanager",
			Access: &AddonAccess{Service: "kube-prometheus-stack-grafana", Port: 80,
				Hint: "usuario admin; contraseña: la elegida al instalar (o prom-operator por defecto)"},
			Params: []AddonParam{
				{Key: "grafanaPassword", Label: "Contraseña de Grafana (admin)", Type: "password",
					Default: "", Path: "grafana.adminPassword"},
				{Key: "retention", Label: "Retención de Prometheus", Type: "string",
					Default: "10d", Path: "prometheus.prometheusSpec.retention"},
			}},
	}
}

// AppSpec describe el proyecto GitOps a registrar (crea una Application de ArgoCD).
type AppSpec struct {
	Name      string `json:"name"`               // nombre de la Application
	RepoURL   string `json:"repoURL"`            // repo Git
	Path      string `json:"path"`               // ruta dentro del repo
	Namespace string `json:"namespace"`          // namespace destino en el clúster
	Revision  string `json:"revision,omitempty"` // rama/tag (por defecto HEAD)
}

// ActionRequest es lo que la GUI envía para encolar una acción.
type ActionRequest struct {
	Kind         string            `json:"kind"`             // scale | restart | install | addapp
	Namespace    string            `json:"namespace"`        // namespace de la carga (scale/restart)
	Workload     string            `json:"workload"`         // nombre de la carga (scale/restart)
	WorkloadKind string            `json:"workloadKind"`     // Deployment | StatefulSet
	Replicas     int               `json:"replicas"`         // objetivo (solo scale)
	Addon        string            `json:"addon,omitempty"`  // complemento a instalar (solo install)
	Values       map[string]string `json:"values,omitempty"` // valores del complemento (solo install)
	App          *AppSpec          `json:"app,omitempty"`    // proyecto a registrar (solo addapp)
	Issuer       *IssuerSpec       `json:"issuer,omitempty"` // emisor TLS a crear (solo issuer)
	Expose       *ExposeSpec       `json:"expose,omitempty"` // servicio a publicar (solo expose)
	Node         string            `json:"node,omitempty"`   // nodo objetivo (cordon/uncordon/drain)
	NS           *NamespaceSpec    `json:"ns,omitempty"`     // namespace a crear (solo createns)
}

// Action es una orden con su estado, tal como la ve el agente y la GUI.
type Action struct {
	ID           string            `json:"id"`
	Kind         string            `json:"kind"`
	Namespace    string            `json:"namespace"`
	Workload     string            `json:"workload"`
	WorkloadKind string            `json:"workloadKind"`
	Replicas     int               `json:"replicas"`
	Addon        string            `json:"addon,omitempty"`
	Values       map[string]string `json:"values,omitempty"`
	App          *AppSpec          `json:"app,omitempty"`
	Issuer       *IssuerSpec       `json:"issuer,omitempty"`
	Expose       *ExposeSpec       `json:"expose,omitempty"`
	Node         string            `json:"node,omitempty"`
	NS           *NamespaceSpec    `json:"ns,omitempty"`
	Output       string            `json:"output,omitempty"` // salida de logs/events
	Status       string            `json:"status"`
	Error        string            `json:"error,omitempty"`
	RequestedBy  string            `json:"requestedBy,omitempty"` // usuario que la pidió (OIDC)
	CreatedAt    time.Time         `json:"createdAt"`
	UpdatedAt    time.Time         `json:"updatedAt"`
}

// ---- Auditoría: rastro de quién hizo qué ----

// Eventos de auditoría.
const (
	AuditRequested = "action.requested" // un usuario encoló una acción
	AuditExecuted  = "action.executed"  // el agente la ejecutó (ok/error)
	AuditMapEdited = "map.edited"       // un usuario editó metadatos del mapa
	AuditLogin     = "auth.login"       // intento de login local (ok o fallido)
	AuditUser      = "user.managed"     // alta/baja de un usuario local
)

// LocalUser es un usuario local de Atlas creado desde la GUI (además del admin
// del instalador). El hash bcrypt NUNCA viaja por la API.
type LocalUser struct {
	Username  string    `json:"username"`
	Role      string    `json:"role"` // viewer | operator
	CreatedBy string    `json:"createdBy,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// AuditEntry es una línea del registro de auditoría.
type AuditEntry struct {
	ID        string    `json:"id"`
	Time      time.Time `json:"time"`
	Actor     string    `json:"actor"` // email del usuario (o "dev" sin auth)
	Event     string    `json:"event"`
	Cluster   string    `json:"cluster"`
	Namespace string    `json:"namespace"`
	Workload  string    `json:"workload"`
	Summary   string    `json:"summary"` // "escalar web a 8", "reiniciar api"
	Outcome   string    `json:"outcome"` // pending | ok | error
	Error     string    `json:"error,omitempty"`
}

// ActionResult lo reporta el agente tras intentar ejecutar una acción.
type ActionResult struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Output: salida de las acciones de diagnóstico (logs / events). Acotada
	// por el agente (MaxActionOutput) para no inflar latidos ni almacenamiento.
	Output string `json:"output,omitempty"`
}

// MaxActionOutput acota la salida de una acción de diagnóstico (bytes).
const MaxActionOutput = 64 * 1024

// ---- Anotaciones: metadatos editables del mapa ----

// Annotation es metadato que la GUI superpone a una entidad del mapa (clúster o
// carga). NO toca el clúster real: es solo la capa de presentación/curación.
// La clave es estable: "clusterId" para un clúster, "clusterId/namespace/workload"
// para una carga.
type Annotation struct {
	DisplayName string `json:"displayName,omitempty"` // alias que se muestra
	Color       string `json:"color,omitempty"`       // color de acento
	Note        string `json:"note,omitempty"`        // nota libre (dueño, etc.)
}

// Empty indica si la anotación no aporta nada (equivale a borrarla).
func (a Annotation) Empty() bool {
	return a.DisplayName == "" && a.Color == "" && a.Note == ""
}

// ---- Vista para la GUI (control plane -> GUI) ----

// ClusterView es lo que el control plane expone a la GUI por cada clúster.
type ClusterView struct {
	ClusterID    string    `json:"clusterId"`
	Name         string    `json:"name"`
	Provider     Provider  `json:"provider"`
	Online       bool      `json:"online"`
	LastSeen     time.Time `json:"lastSeen"`
	AgentVersion string    `json:"agentVersion"`
	Snapshot     Snapshot  `json:"snapshot"`
}

// Topology es la respuesta agregada de /v1/topology que consume el mapa.
type Topology struct {
	Clusters    []ClusterView `json:"clusters"`
	GeneratedAt time.Time     `json:"generatedAt"`
}
