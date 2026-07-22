package controlplane

import (
	"testing"
	"time"

	"github.com/D4rksit3/atlas/pkg/api"
)

func TestRegisterAndHeartbeat(t *testing.T) {
	s := NewMemStore(30 * time.Second)
	now := time.Now()

	token, err := s.Register(api.RegisterRequest{
		ClusterID: "c1", Name: "clúster 1", Provider: api.ProviderOnPrem,
	}, now)
	if err != nil {
		t.Fatalf("register falló: %v", err)
	}
	if token == "" {
		t.Fatal("esperaba un token no vacío")
	}

	snap := api.Snapshot{Nodes: []api.Node{{Name: "n1", Role: "worker", Ready: true}}}
	if err := s.Heartbeat("c1", token, snap, now); err != nil {
		t.Fatalf("heartbeat válido falló: %v", err)
	}

	topo, err := s.Topology(now)
	if err != nil {
		t.Fatalf("topology falló: %v", err)
	}
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
	s := NewMemStore(time.Minute)
	now := time.Now()
	if _, err := s.Register(api.RegisterRequest{ClusterID: "c1", Name: "c1"}, now); err != nil {
		t.Fatalf("register falló: %v", err)
	}

	if err := s.Heartbeat("c1", "token-incorrecto", api.Snapshot{}, now); err != ErrBadToken {
		t.Fatalf("esperaba ErrBadToken, obtuve %v", err)
	}
	if err := s.Heartbeat("inexistente", "x", api.Snapshot{}, now); err != ErrUnknownCluster {
		t.Fatalf("esperaba ErrUnknownCluster, obtuve %v", err)
	}
}

func TestClusterGoesOfflineAfterThreshold(t *testing.T) {
	s := NewMemStore(10 * time.Second)
	now := time.Now()
	token, _ := s.Register(api.RegisterRequest{ClusterID: "c1", Name: "c1"}, now)
	_ = s.Heartbeat("c1", token, api.Snapshot{}, now)

	later := now.Add(20 * time.Second) // más allá del umbral
	topo, err := s.Topology(later)
	if err != nil {
		t.Fatalf("topology falló: %v", err)
	}
	if topo.Clusters[0].Online {
		t.Error("el clúster debería marcarse offline tras el umbral sin latidos")
	}
}

// TestValidActionRequestIssuer cubre el vetado de la acción 'issuer': email con
// formato mínimo y entorno ACME dentro del catálogo cerrado. Es la barrera que
// impide crear un ClusterIssuer mal formado o apuntando a un ACME arbitrario.
func TestValidActionRequestIssuer(t *testing.T) {
	cases := []struct {
		name string
		req  api.ActionRequest
		ok   bool
	}{
		{"válido staging", api.ActionRequest{Kind: api.ActionIssuer,
			Issuer: &api.IssuerSpec{Email: "ops@ich.edu.pe", Environment: "staging"}}, true},
		{"válido production", api.ActionRequest{Kind: api.ActionIssuer,
			Issuer: &api.IssuerSpec{Email: "ops@ich.edu.pe", Environment: "production"}}, true},
		{"sin issuer", api.ActionRequest{Kind: api.ActionIssuer}, false},
		{"email sin @", api.ActionRequest{Kind: api.ActionIssuer,
			Issuer: &api.IssuerSpec{Email: "ops-ich.edu.pe", Environment: "staging"}}, false},
		{"entorno inválido", api.ActionRequest{Kind: api.ActionIssuer,
			Issuer: &api.IssuerSpec{Email: "ops@ich.edu.pe", Environment: "prod"}}, false},
		{"entorno vacío", api.ActionRequest{Kind: api.ActionIssuer,
			Issuer: &api.IssuerSpec{Email: "ops@ich.edu.pe"}}, false},
	}
	for _, c := range cases {
		err := validActionRequest(c.req)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v, esperaba ok=%v", c.name, err, c.ok)
		}
	}
}

func TestReRegisterKeepsPreviousSnapshot(t *testing.T) {
	s := NewMemStore(time.Minute)
	now := time.Now()
	token, _ := s.Register(api.RegisterRequest{ClusterID: "c1", Name: "c1"}, now)
	_ = s.Heartbeat("c1", token, api.Snapshot{Workloads: []api.Workload{{Name: "web"}}}, now)

	// Un re-registro (p. ej. el control plane reinició) no debe borrar el mapa.
	if _, err := s.Register(api.RegisterRequest{ClusterID: "c1", Name: "c1"}, now); err != nil {
		t.Fatalf("re-register falló: %v", err)
	}
	topo, _ := s.Topology(now)
	if len(topo.Clusters[0].Snapshot.Workloads) != 1 {
		t.Error("el re-registro debería conservar el último snapshot conocido")
	}
}
