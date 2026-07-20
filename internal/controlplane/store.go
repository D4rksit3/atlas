package controlplane

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/atlasctl/atlas/pkg/api"
)

// Errores de dominio que el servidor traduce a códigos HTTP.
var (
	ErrUnknownCluster = errors.New("clúster desconocido")
	ErrBadToken       = errors.New("token inválido")
)

// clusterState es el estado interno de un clúster registrado.
type clusterState struct {
	view  api.ClusterView
	token string
}

// Store es el registro en memoria de clústeres. Para el MVP basta con esto;
// cuando quieras persistencia y multi-réplica, cámbialo por Postgres detrás de
// esta misma interfaz.
type Store struct {
	mu           sync.RWMutex
	clusters     map[string]*clusterState
	offlineAfter time.Duration
}

// NewStore crea un registro vacío. offlineAfter es el tiempo sin latidos tras
// el cual un clúster se marca como offline.
func NewStore(offlineAfter time.Duration) *Store {
	return &Store{
		clusters:     make(map[string]*clusterState),
		offlineAfter: offlineAfter,
	}
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Register da de alta (o re-registra) un clúster y devuelve un token de sesión.
// Conserva el último snapshot conocido para no "parpadear" en la GUI.
func (s *Store) Register(req api.RegisterRequest, now time.Time) string {
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
	return cs.token
}

// Heartbeat actualiza el snapshot de un clúster tras validar su token.
func (s *Store) Heartbeat(clusterID, token string, snap api.Snapshot, now time.Time) error {
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

// Topology devuelve la vista agregada, marcando offline lo que lleve demasiado
// tiempo sin latir.
func (s *Store) Topology(now time.Time) api.Topology {
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
	return out
}
