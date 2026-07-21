package controlplane

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/atlasctl/atlas/pkg/api"
)

// clusterState es el estado interno de un clúster registrado en memoria.
type clusterState struct {
	view    api.ClusterView
	token   string
	actions []api.Action // cola + historial de acciones
}

// MemStore es el registro en memoria de clústeres. Simple y sin dependencias;
// ideal para desarrollo y despliegues de una sola réplica. Para persistencia y
// multi-réplica usa PgStore (misma interfaz Store).
type MemStore struct {
	mu           sync.RWMutex
	clusters     map[string]*clusterState
	offlineAfter time.Duration
}

// NewMemStore crea un registro vacío. offlineAfter es el tiempo sin latidos tras
// el cual un clúster se marca como offline.
func NewMemStore(offlineAfter time.Duration) *MemStore {
	return &MemStore{
		clusters:     make(map[string]*clusterState),
		offlineAfter: offlineAfter,
	}
}

func (s *MemStore) Register(req api.RegisterRequest, now time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cs, ok := s.clusters[req.ClusterID]
	if !ok {
		cs = &clusterState{}
		s.clusters[req.ClusterID] = cs
	}
	cs.token = newToken()
	prevSnapshot := cs.view.Snapshot
	cs.view = api.ClusterView{
		ClusterID:    req.ClusterID,
		Name:         req.Name,
		Provider:     req.Provider,
		AgentVersion: req.AgentVersion,
		Online:       true,
		LastSeen:     now,
		Snapshot:     prevSnapshot,
	}
	return cs.token, nil
}

func (s *MemStore) Heartbeat(clusterID, token string, snap api.Snapshot, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cs, ok := s.clusters[clusterID]
	if !ok {
		return ErrUnknownCluster
	}
	if cs.token != token {
		return ErrBadToken
	}
	cs.view.Snapshot = snap
	cs.view.Online = true
	cs.view.LastSeen = now
	return nil
}

func (s *MemStore) Topology(now time.Time) (api.Topology, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := api.Topology{
		GeneratedAt: now,
		Clusters:    make([]api.ClusterView, 0, len(s.clusters)),
	}
	for _, cs := range s.clusters {
		v := cs.view
		if now.Sub(v.LastSeen) > s.offlineAfter {
			v.Online = false
		}
		out.Clusters = append(out.Clusters, v)
	}
	// Orden estable para que la GUI no reordene nodos en cada poll.
	sort.Slice(out.Clusters, func(i, j int) bool {
		return out.Clusters[i].ClusterID < out.Clusters[j].ClusterID
	})
	return out, nil
}

func (s *MemStore) EnqueueAction(clusterID string, req api.ActionRequest, now time.Time) (api.Action, error) {
	if err := validActionRequest(req); err != nil {
		return api.Action{}, fmt.Errorf("%w: %v", ErrBadAction, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cs, ok := s.clusters[clusterID]
	if !ok {
		return api.Action{}, ErrUnknownCluster
	}
	a := api.Action{
		ID: newActionID(), Kind: req.Kind, Namespace: req.Namespace,
		Workload: req.Workload, WorkloadKind: req.WorkloadKind, Replicas: req.Replicas,
		Status: api.ActionPending, CreatedAt: now, UpdatedAt: now,
	}
	cs.actions = append(cs.actions, a)
	return a, nil
}

func (s *MemStore) TakeActions(clusterID string, now time.Time) ([]api.Action, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs, ok := s.clusters[clusterID]
	if !ok {
		return nil, ErrUnknownCluster
	}
	var pending []api.Action
	for i := range cs.actions {
		if cs.actions[i].Status == api.ActionPending {
			cs.actions[i].Status = api.ActionDispatched
			cs.actions[i].UpdatedAt = now
			pending = append(pending, cs.actions[i])
		}
	}
	return pending, nil
}

func (s *MemStore) RecordResults(clusterID string, results []api.ActionResult, now time.Time) error {
	if len(results) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cs, ok := s.clusters[clusterID]
	if !ok {
		return ErrUnknownCluster
	}
	byID := make(map[string]api.ActionResult, len(results))
	for _, r := range results {
		byID[r.ID] = r
	}
	for i := range cs.actions {
		if r, ok := byID[cs.actions[i].ID]; ok {
			if r.OK {
				cs.actions[i].Status = api.ActionDone
			} else {
				cs.actions[i].Status = api.ActionError
				cs.actions[i].Error = r.Error
			}
			cs.actions[i].UpdatedAt = now
		}
	}
	return nil
}

func (s *MemStore) ListActions(clusterID string) ([]api.Action, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cs, ok := s.clusters[clusterID]
	if !ok {
		return nil, ErrUnknownCluster
	}
	out := make([]api.Action, len(cs.actions))
	copy(out, cs.actions)
	return out, nil
}
