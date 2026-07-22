// Alertas: Atlas vigila la topología y AVISA — el panel deja de ser algo que
// hay que mirar. La evaluación es PURA (sin estado): las alertas activas se
// derivan del último snapshot de cada clúster. El estado solo existe para
// notificar por webhook en los FLANCOS (aparece / se resuelve), no en cada tick.
package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/atlasctl/atlas/pkg/api"
)

// EvaluateAlerts deriva las alertas activas de una topología. Determinista y
// sin efectos: se puede llamar en cada GET /v1/alerts.
func EvaluateAlerts(t api.Topology, now time.Time) []api.Alert {
	var out []api.Alert
	add := func(kind, severity, cluster, resource, msg string) {
		out = append(out, api.Alert{
			ID:       kind + "/" + cluster + "/" + resource,
			Kind:     kind, Severity: severity, Cluster: cluster,
			Resource: resource, Message: msg, Since: now,
		})
	}
	for _, c := range t.Clusters {
		if !c.Online {
			add("cluster-offline", api.SeverityCritical, c.ClusterID, c.Name,
				fmt.Sprintf("el clúster %q está OFFLINE (sin latidos del agente)", c.Name))
			continue // sin latidos, el snapshot está viejo: no evaluar su contenido
		}
		for _, n := range c.Snapshot.Nodes {
			if !n.Ready {
				add("node-notready", api.SeverityCritical, c.ClusterID, n.Name,
					fmt.Sprintf("el nodo %q está NotReady", n.Name))
			}
		}
		for _, w := range c.Snapshot.Workloads {
			for _, p := range w.Pods {
				switch p.Reason {
				case "CrashLoopBackOff":
					add("pod-crashloop", api.SeverityCritical, c.ClusterID, w.Namespace+"/"+p.Name,
						fmt.Sprintf("el pod %s/%s está en CrashLoopBackOff (%d reinicios) — mira sus logs", w.Namespace, p.Name, p.Restarts))
				case "ImagePullBackOff", "ErrImagePull":
					add("pod-imagepull", api.SeverityWarning, c.ClusterID, w.Namespace+"/"+p.Name,
						fmt.Sprintf("el pod %s/%s no puede descargar su imagen (%s)", w.Namespace, p.Name, p.Reason))
				}
			}
		}
	}
	return out
}

// Alerter corre en segundo plano: evalúa cada intervalo y notifica por webhook
// los flancos (alerta nueva / alerta resuelta). Con webhook vacío solo mantiene
// el estado (que GET /v1/alerts usa para conservar el 'since' original).
type Alerter struct {
	store   Store
	webhook string
	client  *http.Client

	mu     sync.RWMutex
	active map[string]api.Alert // alertas vivas, con su 'since' original
}

// NewAlerter construye el vigilante. webhook puede ser "".
func NewAlerter(store Store, webhook string) *Alerter {
	return &Alerter{
		store: store, webhook: webhook,
		client: &http.Client{Timeout: 10 * time.Second},
		active: make(map[string]api.Alert),
	}
}

// Current devuelve las alertas activas con su 'since' estable.
func (a *Alerter) Current() []api.Alert {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]api.Alert, 0, len(a.active))
	for _, al := range a.active {
		out = append(out, al)
	}
	return out
}

// Run evalúa en bucle hasta que el contexto muera.
func (a *Alerter) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 30 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.tick(time.Now())
		}
	}
}

func (a *Alerter) tick(now time.Time) {
	topo, err := a.store.Topology(now)
	if err != nil {
		log.Printf("alertas: no pude leer la topología: %v", err)
		return
	}
	fresh := EvaluateAlerts(topo, now)

	a.mu.Lock()
	seen := make(map[string]bool, len(fresh))
	var fired, resolved []api.Alert
	for _, al := range fresh {
		seen[al.ID] = true
		if prev, ok := a.active[al.ID]; ok {
			al.Since = prev.Since // conserva desde cuándo está viva
		} else {
			fired = append(fired, al)
		}
		a.active[al.ID] = al
	}
	for id, al := range a.active {
		if !seen[id] {
			resolved = append(resolved, al)
			delete(a.active, id)
		}
	}
	a.mu.Unlock()

	for _, al := range fired {
		log.Printf("ALERTA [%s] %s", al.Severity, al.Message)
		a.notify("alert.fired", al)
	}
	for _, al := range resolved {
		log.Printf("alerta resuelta: %s", al.Message)
		a.notify("alert.resolved", al)
	}
}

// notify envía el evento al webhook (si hay). Formato genérico JSON — sirve
// para Slack/Discord vía proxy, n8n, o cualquier receptor propio.
func (a *Alerter) notify(event string, al api.Alert) {
	if a.webhook == "" {
		return
	}
	body, _ := json.Marshal(map[string]any{
		"event": event,
		"alert": al,
		"text":  fmt.Sprintf("[atlas] %s: %s", event, al.Message),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.webhook, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		log.Printf("alertas: webhook falló: %v", err)
		return
	}
	resp.Body.Close()
}
