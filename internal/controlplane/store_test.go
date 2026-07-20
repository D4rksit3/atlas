package controlplane

import (
	"testing"
	"time"

	"github.com/atlasctl/atlas/pkg/api"
)

func TestRegisterAndHeartbeat(t *testing.T) {
	s := NewStore(30 * time.Second)
	now := time.Now()

	token := s.Register(api.RegisterRequest{
		ClusterID: "c1", Name: "clúster 1", Provider: api.ProviderOnPrem,
	}, now)
	if token == "" {
		t.Fatal("esperaba un token no vacío")
	}

	snap := api.Snapshot{Nodes: []api.Node{{Name: "n1", Role: "worker", Ready: true}}}
	if err := s.Heartbeat("c1", token, snap, now); err != nil {
		t.Fatalf("heartbeat válido falló: %v", err)
	}

	topo := s.Topology(now)
	if len(topo.Clusters) != 1 {
		t.Fatalf("esperaba 1 clúster, hay %d", len(topo.Clusters))
	}
	if !topo.Clusters[0].Online {
		t.Error("el clúster debería estar online tras latir")
	}
	if got := len(topo.Clusters[0].Snapshot.Nodes); got != 1 {
		t.Errorf("esperaba 1 nodo en el snapshot, hay %d", got)
	}
}

func TestHeartbeatRejectsBadTokenAndUnknownCluster(t *testing.T) {
	s := NewStore(time.Minute)
	now := time.Now()
	s.Register(api.RegisterRequest{ClusterID: "c1", Name: "c1"}, now)

	if err := s.Heartbeat("c1", "token-incorrecto", api.Snapshot{}, now); err != ErrBadToken {
		t.Fatalf("esperaba ErrBadToken, obtuve %v", err)
	}
	if err := s.Heartbeat("inexistente", "x", api.Snapshot{}, now); err != ErrUnknownCluster {
		t.Fatalf("esperaba ErrUnknownCluster, obtuve %v", err)
	}
}

func TestClusterGoesOfflineAfterThreshold(t *testing.T) {
	s := NewStore(10 * time.Second)
	now := time.Now()
	token := s.Register(api.RegisterRequest{ClusterID: "c1", Name: "c1"}, now)
	_ = s.Heartbeat("c1", token, api.Snapshot{}, now)

	later := now.Add(20 * time.Second) // más allá del umbral
	topo := s.Topology(later)
	if topo.Clusters[0].Online {
		t.Error("el clúster debería marcarse offline tras el umbral sin latidos")
	}
}

func TestReRegisterKeepsPreviousSnapshot(t *testing.T) {
	s := NewStore(time.Minute)
	now := time.Now()
	token := s.Register(api.RegisterRequest{ClusterID: "c1", Name: "c1"}, now)
	_ = s.Heartbeat("c1", token, api.Snapshot{Workloads: []api.Workload{{Name: "web"}}}, now)

	// Un re-registro (p. ej. el control plane reinició) no debe borrar el mapa.
	s.Register(api.RegisterRequest{ClusterID: "c1", Name: "c1"}, now)
	topo := s.Topology(now)
	if len(topo.Clusters[0].Snapshot.Workloads) != 1 {
		t.Error("el re-registro debería conservar el último snapshot conocido")
	}
}
