package controlplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/atlasctl/atlas/pkg/api"
)

// topoDe construye una topología mínima para los tests de alertas.
func topoDe(online bool, nodeReady bool, podReason string) api.Topology {
	return api.Topology{Clusters: []api.ClusterView{{
		ClusterID: "c1", Name: "prod", Online: online,
		Snapshot: api.Snapshot{
			Nodes: []api.Node{{Name: "n1", Ready: nodeReady}},
			Workloads: []api.Workload{{
				Name: "web", Namespace: "default", Kind: "Deployment", Replicas: 1,
				Pods: []api.PodInfo{{Name: "web-1", Phase: "Running", Reason: podReason, Restarts: 7}},
			}},
		},
	}}}
}

func alertKinds(alerts []api.Alert) map[string]bool {
	out := map[string]bool{}
	for _, a := range alerts {
		out[a.Kind] = true
	}
	return out
}

func TestEvaluateAlerts(t *testing.T) {
	now := time.Now()

	// Sano: sin alertas.
	if got := EvaluateAlerts(topoDe(true, true, ""), now); len(got) != 0 {
		t.Fatalf("clúster sano no debería alertar: %v", got)
	}
	// Offline: alerta crítica y NO se evalúa el contenido viejo del snapshot.
	got := EvaluateAlerts(topoDe(false, false, "CrashLoopBackOff"), now)
	if len(got) != 1 || got[0].Kind != "cluster-offline" || got[0].Severity != api.SeverityCritical {
		t.Fatalf("esperaba solo cluster-offline crítica: %v", got)
	}
	// Nodo NotReady + pod en CrashLoop: dos alertas.
	kinds := alertKinds(EvaluateAlerts(topoDe(true, false, "CrashLoopBackOff"), now))
	if !kinds["node-notready"] || !kinds["pod-crashloop"] {
		t.Fatalf("esperaba node-notready y pod-crashloop: %v", kinds)
	}
	// ImagePull: warning.
	got = EvaluateAlerts(topoDe(true, true, "ImagePullBackOff"), now)
	if len(got) != 1 || got[0].Kind != "pod-imagepull" || got[0].Severity != api.SeverityWarning {
		t.Fatalf("esperaba pod-imagepull warning: %v", got)
	}
}

// TestAlerterWebhookFlancos verifica que el webhook solo recibe FLANCOS:
// una vez al aparecer la alerta (aunque siga activa N ticks) y una al resolverse.
func TestAlerterWebhookFlancos(t *testing.T) {
	var mu sync.Mutex
	var events []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Event string `json:"event"`
			Alert api.Alert
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		events = append(events, body.Event+":"+body.Alert.Kind)
		mu.Unlock()
	}))
	defer hook.Close()

	store := NewMemStore(30 * time.Second)
	now := time.Now()
	if _, err := store.Register(api.RegisterRequest{ClusterID: "c1", Name: "prod"}, now); err != nil {
		t.Fatal(err)
	}
	a := NewAlerter(store, hook.URL)

	// Tick 1: el clúster acaba de registrarse pero AÚN no late → online (Register
	// lo marca online). Simula caída: un tick con LastSeen viejo.
	a.tick(now.Add(2 * time.Minute)) // 2min sin latidos > offlineAfter → offline
	a.tick(now.Add(3 * time.Minute)) // sigue caído: NO debe re-notificar
	if got := a.Current(); len(got) != 1 || got[0].Kind != "cluster-offline" {
		t.Fatalf("esperaba cluster-offline activa: %v", got)
	}

	// Vuelve a latir → la alerta se resuelve.
	tok, _ := store.Register(api.RegisterRequest{ClusterID: "c1", Name: "prod"}, now.Add(4*time.Minute))
	_ = tok
	a.tick(now.Add(4 * time.Minute))
	if got := a.Current(); len(got) != 0 {
		t.Fatalf("no debería quedar alerta activa: %v", got)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"alert.fired:cluster-offline", "alert.resolved:cluster-offline"}
	if len(events) != 2 || events[0] != want[0] || events[1] != want[1] {
		t.Fatalf("flancos esperados %v, obtuve %v", want, events)
	}
}
