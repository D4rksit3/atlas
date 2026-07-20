package controlplane

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// Metrics son contadores en memoria expuestos en /metrics en formato Prometheus.
// Sin dependencias externas: mantiene el binario pequeño y el scaffold simple.
// Cuando quieras histogramas/labels ricos, cámbialo por prometheus/client_golang
// detrás de esta misma superficie.
type Metrics struct {
	Requests        atomic.Int64
	Registers       atomic.Int64
	Heartbeats      atomic.Int64
	HeartbeatErrors atomic.Int64
}

// NewMetrics crea un registro de métricas vacío.
func NewMetrics() *Metrics { return &Metrics{} }

// WriteProm escribe las métricas en texto Prometheus (v0.0.4). Combina los
// contadores acumulados con gauges vivos calculados desde el store.
func (m *Metrics) WriteProm(w io.Writer, store *Store) {
	topo := store.Topology(time.Now())
	total := len(topo.Clusters)
	online := 0
	for _, c := range topo.Clusters {
		if c.Online {
			online++
		}
	}

	metric(w, "atlas_http_requests_total", "Total de peticiones HTTP recibidas.", "counter", m.Requests.Load())
	metric(w, "atlas_agent_registrations_total", "Registros de agentes procesados.", "counter", m.Registers.Load())
	metric(w, "atlas_heartbeats_total", "Latidos aceptados.", "counter", m.Heartbeats.Load())
	metric(w, "atlas_heartbeat_errors_total", "Latidos rechazados (token/clúster inválido).", "counter", m.HeartbeatErrors.Load())
	metric(w, "atlas_clusters_total", "Clústeres registrados.", "gauge", int64(total))
	metric(w, "atlas_clusters_online", "Clústeres online (con latido reciente).", "gauge", int64(online))
}

func metric(w io.Writer, name, help, typ string, val int64) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %d\n", name, help, name, typ, name, val)
}
