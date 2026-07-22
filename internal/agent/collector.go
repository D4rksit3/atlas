package agent

import (
	"math/rand"

	"github.com/D4rksit3/atlas/pkg/api"
)

// Collector obtiene el estado del clúster donde corre el agente.
// Collect puede fallar (p. ej. la API de K8s no responde); el agente decide
// qué hacer con el error (hoy: registra y salta ese latido).
type Collector interface {
	Collect() (api.Snapshot, error)
}

// LinkProvider aporta las CONEXIONES entre servicios (los Links del mapa), que
// no viven en la API de Kubernetes sino en la observabilidad de red (Hubble).
type LinkProvider interface {
	Links() ([]api.Link, error)
}

// withLinks compone un Collector (nodos + cargas) con un LinkProvider (enlaces):
// el snapshot base se enriquece con los enlaces reales. Si el proveedor falla,
// NO se cae el latido: se devuelve el snapshot sin enlaces y se anota el error.
type withLinks struct {
	base  Collector
	links LinkProvider
	log   func(string, ...any)
}

// WithLinks devuelve un Collector que añade enlaces del provider al base.
func WithLinks(base Collector, links LinkProvider, logf func(string, ...any)) Collector {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &withLinks{base: base, links: links, log: logf}
}

func (c *withLinks) Collect() (api.Snapshot, error) {
	snap, err := c.base.Collect()
	if err != nil {
		return snap, err // el base manda: sin nodos/cargas no hay mapa
	}
	links, err := c.links.Links()
	if err != nil {
		// Los enlaces son "mejor esfuerzo": el mapa sigue vivo sin ellos.
		c.log("proveedor de enlaces falló (sigo sin enlaces): %v", err)
		return snap, nil
	}
	snap.Links = links
	return snap, nil
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
		{Name: "cp-0", Role: "control-plane", Ready: true, Usage: &api.Usage{CPUm: 210, MemMi: 900}},
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
		{Name: "web", Namespace: "default", Kind: "Deployment", Replicas: 3,
			Usage: &api.Usage{CPUm: 34, MemMi: 180}, Pods: []api.PodInfo{
			{Name: "web-7f9c-a1", IP: "10.42.0.11", Node: "cp-0", Phase: "Running"},
			{Name: "web-7f9c-b2", IP: "10.42.1.12", Node: workerName(c.provider, 0), Phase: "Running"},
			{Name: "web-7f9c-c3", IP: "10.42.1.13", Node: workerName(c.provider, 0), Phase: "Running"},
		}},
		{Name: "api", Namespace: "default", Kind: "Deployment", Replicas: 2,
			Usage: &api.Usage{CPUm: 58, MemMi: 240}, Pods: []api.PodInfo{
			{Name: "api-55d8-x9", IP: "10.42.0.21", Node: "cp-0", Phase: "Running"},
			{Name: "api-55d8-y7", IP: "10.42.1.22", Node: workerName(c.provider, 0), Phase: "Running"},
		}},
		{Name: "postgres", Namespace: "data", Kind: "StatefulSet", Replicas: 1,
			Usage: &api.Usage{CPUm: 21, MemMi: 310}, Pods: []api.PodInfo{
			{Name: "postgres-0", IP: "10.42.1.31", Node: workerName(c.provider, 0), Phase: "Running"},
		}},
	}
	services := []api.ServiceInfo{
		{Name: "web", Namespace: "default", Type: "ClusterIP", ClusterIP: "10.43.10.20",
			Ports: []api.ServicePort{{Port: 80, Protocol: "TCP"}}, Workloads: []string{"web"}},
		{Name: "api", Namespace: "default", Type: "ClusterIP", ClusterIP: "10.43.10.30",
			Ports: []api.ServicePort{{Port: 8080, Protocol: "TCP"}}, Workloads: []string{"api"}},
		{Name: "postgres", Namespace: "data", Type: "Headless",
			Ports: []api.ServicePort{{Port: 5432, Protocol: "TCP"}}, Workloads: []string{"postgres"}},
	}
	links := []api.Link{
		{From: "web", To: "api"},
		{From: "api", To: "postgres"},
	}
	// Una ruta publicada de ejemplo, para que el módulo Servicios y la vista
	// Administrar tengan algo que enseñar también con datos ficticios.
	ingresses := []api.IngressInfo{
		{Name: "atlas-web", Namespace: "default", Class: "nginx",
			Host: "web.ejemplo.local", Path: "/", Service: "web", Port: 80},
	}

	return api.Snapshot{
		Nodes: nodes, Workloads: workloads, Links: links,
		Ingresses: ingresses, Services: services,
	}, nil
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
