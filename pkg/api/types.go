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
}

// Placement dice cuántos pods de una carga corren en un nodo concreto. Una carga
// puede repartir sus réplicas entre varios nodos, así que lleva una lista.
type Placement struct {
	Node string `json:"node"`
	Pods int    `json:"pods"`
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
}

// Link es una conexión observada entre dos cargas (fuente de datos: Hubble).
type Link struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Snapshot es la foto del clúster que el agente envía en cada heartbeat.
type Snapshot struct {
	Nodes     []Node     `json:"nodes"`
	Workloads []Workload `json:"workloads"`
	Links     []Link     `json:"links"`
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
	ActionScale   = "scale"   // cambiar el nº de réplicas de una carga
	ActionRestart = "restart" // reinicio suave (rollout) de una carga
	ActionInstall = "install" // instalar un complemento vetado (p. ej. ArgoCD)
)

// ActionRequest es lo que la GUI envía para encolar una acción.
type ActionRequest struct {
	Kind         string `json:"kind"`            // scale | restart | install
	Namespace    string `json:"namespace"`       // namespace de la carga (scale/restart)
	Workload     string `json:"workload"`        // nombre de la carga (scale/restart)
	WorkloadKind string `json:"workloadKind"`    // Deployment | StatefulSet
	Replicas     int    `json:"replicas"`        // objetivo (solo scale)
	Addon        string `json:"addon,omitempty"` // complemento a instalar (solo install)
}

// Action es una orden con su estado, tal como la ve el agente y la GUI.
type Action struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	Namespace    string    `json:"namespace"`
	Workload     string    `json:"workload"`
	WorkloadKind string    `json:"workloadKind"`
	Replicas     int       `json:"replicas"`
	Addon        string    `json:"addon,omitempty"`
	Status       string    `json:"status"`
	Error        string    `json:"error,omitempty"`
	RequestedBy  string    `json:"requestedBy,omitempty"` // usuario que la pidió (OIDC)
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// ---- Auditoría: rastro de quién hizo qué ----

// Eventos de auditoría.
const (
	AuditRequested = "action.requested" // un usuario encoló una acción
	AuditExecuted  = "action.executed"  // el agente la ejecutó (ok/error)
	AuditMapEdited = "map.edited"       // un usuario editó metadatos del mapa
)

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
}

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
