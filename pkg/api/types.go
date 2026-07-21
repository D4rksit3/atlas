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

// Heartbeat lleva un snapshot del estado del clúster en cada latido.
type Heartbeat struct {
	Token    string   `json:"token"`
	Snapshot Snapshot `json:"snapshot"`
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
