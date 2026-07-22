package agent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/D4rksit3/atlas/pkg/api"
	flowpb "github.com/cilium/cilium/api/v1/flow"
	observerpb "github.com/cilium/cilium/api/v1/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// HubbleCollector obtiene las CONEXIONES reales entre servicios (los Links del
// mapa) desde Hubble (la observabilidad de red de Cilium). La API de Kubernetes
// NO conoce quién habla con quién; Hubble sí, porque observa los flujos L3/L4.
//
// Implementa LinkProvider, no Collector: se COMPONE con el colector kube
// (nodos + cargas) para producir el snapshot completo. Ver withLinks().
type HubbleCollector struct {
	addr    string        // dirección de hubble-relay (p. ej. hubble-relay.kube-system:80)
	sample  int           // cuántos flujos recientes muestrear por latido
	timeout time.Duration // presupuesto por consulta
}

// NewHubbleCollector crea el proveedor de enlaces. addr es hubble-relay; in-cluster
// suele ser "hubble-relay.kube-system:80". Sin TLS (relay interno del clúster).
func NewHubbleCollector(addr string) *HubbleCollector {
	if addr == "" {
		addr = "hubble-relay.kube-system:80"
	}
	return &HubbleCollector{addr: addr, sample: 2000, timeout: 10 * time.Second}
}

// Links consulta los últimos N flujos y los agrega en enlaces workload→workload.
// Solo cuenta tráfico intra-clúster reenviado (FORWARDED) entre cargas con nombre;
// descarta ruido de host/health y flujos sin identidad resoluble.
func (h *HubbleCollector) Links() ([]api.Link, error) {
	ctx, cancel := context.WithTimeout(context.Background(), h.timeout)
	defer cancel()

	conn, err := grpc.DialContext(ctx, h.addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("conectando a hubble-relay %s: %w", h.addr, err)
	}
	defer conn.Close()

	client := observerpb.NewObserverClient(conn)
	stream, err := client.GetFlows(ctx, &observerpb.GetFlowsRequest{
		Number: uint64(h.sample),
		Follow: false, // devuelve los últimos N y termina; no seguimos en vivo
	})
	if err != nil {
		return nil, fmt.Errorf("GetFlows: %w", err)
	}

	// Deduplicamos enlaces dirigidos (from→to) preservando el orden de aparición.
	seen := make(map[string]struct{})
	var links []api.Link
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("recibiendo flujo: %w", err)
		}
		f := resp.GetFlow()
		if f == nil || f.GetVerdict() != flowpb.Verdict_FORWARDED {
			continue // NodeStatus, drops, etc.
		}
		// Solo conexiones INICIADAS, no las respuestas: así el grafo es dirigido
		// (web→api, no api→web) y refleja quién llama a quién.
		if r := f.GetIsReply(); r != nil && r.GetValue() {
			continue
		}
		from := endpointWorkload(f.GetSource())
		to := endpointWorkload(f.GetDestination())
		if from == "" || to == "" || from == to {
			continue // ruido host/health o autoconexión
		}
		key := from + "\x00" + to
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		links = append(links, api.Link{From: from, To: to})
	}
	return links, nil
}

// endpointWorkload resuelve el nombre de la carga (Deployment/StatefulSet) de un
// extremo del flujo, para que coincida con Workload.Name del colector kube.
// Prioridad: nombre de workload que reporta Hubble -> derivarlo del nombre del pod.
// Devuelve "" si el extremo no es una carga del clúster (host, world, health).
func endpointWorkload(e *flowpb.Endpoint) string {
	if e == nil || e.GetNamespace() == "" {
		return "" // fuera del clúster o sin identidad de pod
	}
	if ws := e.GetWorkloads(); len(ws) > 0 && ws[0].GetName() != "" {
		return ws[0].GetName()
	}
	return workloadFromPod(e.GetPodName())
}

// workloadFromPod deriva el nombre de la carga a partir del nombre del pod:
//   - pods de Deployment: "<carga>-<hash-rs>-<hash-pod>" -> quita 2 segmentos
//   - pods de StatefulSet: "<carga>-<ordinal>"           -> quita 1 segmento
func workloadFromPod(pod string) string {
	if pod == "" {
		return ""
	}
	parts := strings.Split(pod, "-")
	switch {
	case len(parts) >= 3:
		return strings.Join(parts[:len(parts)-2], "-")
	case len(parts) == 2:
		return parts[0]
	default:
		return pod
	}
}
