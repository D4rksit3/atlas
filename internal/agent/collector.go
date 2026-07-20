package agent

import (
	"math/rand"

	"github.com/atlasctl/atlas/pkg/api"
)

// Collector obtiene el estado del clúster donde corre el agente.
// Collect puede fallar (p. ej. la API de K8s no responde); el agente decide
// qué hacer con el error (hoy: registra y salta ese latido).
type Collector interface {
	Collect() (api.Snapshot, error)
}

// SampleCollector produce una topología de ejemplo, coherente con el provider,
// para que puedas ver el mapa vivo end-to-end SIN un clúster real todavía.
//
// TODO(fase 1): sustituir por un colector real con client-go:
//   - rest.InClusterConfig() para el kubeconfig del ServiceAccount
//   - listar Nodes, Deployments/StatefulSets por namespace
//   - las conexiones (Links) salen de Hubble (Cilium), no de la API de K8s
type SampleCollector struct {
	provider api.Provider
	nodes    int
}

// NewSampleCollector crea un colector de ejemplo con n nodos worker.
func NewSampleCollector(provider api.Provider, workerNodes int) *SampleCollector {
	if workerNodes < 1 {
		workerNodes = 2
	}
	return &SampleCollector{provider: provider, nodes: workerNodes}
}

func (c *SampleCollector) Collect() (api.Snapshot, error) {
	nodes := []api.Node{
		{Name: "cp-0", Role: "control-plane", Ready: true},
	}
	for i := 0; i < c.nodes; i++ {
		nodes = append(nodes, api.Node{
			Name: workerName(c.provider, i),
			Role: "worker",
			// Un pequeño ruido para que se note que es un mapa "vivo".
			Ready: rand.Float64() > 0.05,
		})
	}

	workloads := []api.Workload{
		{Name: "web", Namespace: "default", Kind: "Deployment", Replicas: 3},
		{Name: "api", Namespace: "default", Kind: "Deployment", Replicas: 2},
		{Name: "postgres", Namespace: "data", Kind: "StatefulSet", Replicas: 1},
	}
	links := []api.Link{
		{From: "web", To: "api"},
		{From: "api", To: "postgres"},
	}

	return api.Snapshot{Nodes: nodes, Workloads: workloads, Links: links}, nil
}

func workerName(p api.Provider, i int) string {
	switch p {
	case api.ProviderAWS:
		return "ip-10-0-1-" + itoa(10+i)
	case api.ProviderOCI:
		return "oke-node-" + itoa(i)
	default:
		return "node-" + itoa(i+1)
	}
}

// itoa evita importar strconv solo para esto.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for n > 0 {
		pos--
		b[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(b[pos:])
}
