package controlplane

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/D4rksit3/atlas/pkg/api"
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
	audit        []api.AuditEntry          // rastro de auditoría (más antiguo primero)
	annotations  map[string]api.Annotation // metadatos del mapa por clave
	users        map[string]localUserRec   // usuarios locales creados desde la GUI
	enrolls      map[string]*enrollState   // tokens de vinculación, por HASH
}

// localUserRec guarda un usuario local con su hash bcrypt (nunca sale de aquí).
type localUserRec struct {
	hash string
	info api.LocalUser
}

// enrollState es un token de vinculación en reposo (solo el hash lo referencia).
type enrollState struct {
	name      string
	provider  api.Provider
	expiresAt time.Time
	used      bool
}

const maxAudit = 1000 // tope del registro en memoria

// NewMemStore crea un registro vacío. offlineAfter es el tiempo sin latidos tras
// el cual un clúster se marca como offline.
func NewMemStore(offlineAfter time.Duration) *MemStore {
	return &MemStore{
		clusters:     make(map[string]*clusterState),
		offlineAfter: offlineAfter,
		annotations:  make(map[string]api.Annotation),
		users:        make(map[string]localUserRec),
		enrolls:      make(map[string]*enrollState),
	}
}

func (s *MemStore) CreateEnrollToken(name string, provider api.Provider, actor string, now time.Time) (api.EnrollToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token := newToken() + newToken() // 256 bits: es una credencial, no un id
	et := api.EnrollToken{Token: token, Name: name, Provider: provider, ExpiresAt: now.Add(EnrollTTL)}
	s.enrolls[hashEnrollToken(token)] = &enrollState{name: name, provider: provider, expiresAt: et.ExpiresAt}
	s.appendAudit(api.AuditEntry{
		ID: newActionID(), Time: now, Actor: actor, Event: api.AuditEnroll,
		Summary: fmt.Sprintf("creó un token de vinculación para %q (caduca %s)", name, et.ExpiresAt.Format(time.RFC3339)),
		Outcome: api.ActionDone,
	})
	return et, nil
}

func (s *MemStore) ConsumeEnrollToken(token string, now time.Time) (api.EnrollToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.enrolls[hashEnrollToken(token)]
	if !ok || e.used || now.After(e.expiresAt) {
		return api.EnrollToken{}, ErrBadEnrollToken
	}
	e.used = true // un solo uso: se quema aunque el resto del enrolamiento falle
	s.appendAudit(api.AuditEntry{
		ID: newActionID(), Time: now, Actor: "enroll", Event: api.AuditEnroll,
		Summary: fmt.Sprintf("token de vinculación canjeado: se emitió certificado para %q", e.name),
		Outcome: api.ActionDone,
	})
	return api.EnrollToken{Name: e.name, Provider: e.provider, ExpiresAt: e.expiresAt}, nil
}

// appendAudit añade una entrada (requiere el lock tomado por el llamador).
func (s *MemStore) appendAudit(e api.AuditEntry) {
	s.audit = append(s.audit, e)
	if len(s.audit) > maxAudit {
		s.audit = s.audit[len(s.audit)-maxAudit:]
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

func (s *MemStore) EnqueueAction(clusterID string, req api.ActionRequest, actor string, now time.Time) (api.Action, error) {
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
		Addon: req.Addon, Values: req.Values, App: req.App, Issuer: req.Issuer,
		Expose: req.Expose, Node: req.Node, NS: req.NS,
		Status: api.ActionPending, RequestedBy: actor, CreatedAt: now, UpdatedAt: now,
	}
	cs.actions = append(cs.actions, a)
	s.appendAudit(api.AuditEntry{
		ID: newActionID(), Time: now, Actor: actor, Event: api.AuditRequested,
		Cluster: clusterID, Namespace: a.Namespace, Workload: a.Workload,
		Summary: summarize(a), Outcome: api.ActionPending,
	})
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
			outcome := api.ActionDone
			if r.OK {
				cs.actions[i].Status = api.ActionDone
			} else {
				cs.actions[i].Status = api.ActionError
				cs.actions[i].Error = r.Error
				outcome = api.ActionError
			}
			cs.actions[i].Output = r.Output
			cs.actions[i].UpdatedAt = now
			s.appendAudit(api.AuditEntry{
				ID: newActionID(), Time: now, Actor: cs.actions[i].RequestedBy,
				Event: api.AuditExecuted, Cluster: clusterID,
				Namespace: cs.actions[i].Namespace, Workload: cs.actions[i].Workload,
				Summary: summarize(cs.actions[i]), Outcome: outcome, Error: r.Error,
			})
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

func (s *MemStore) SetAnnotation(key string, a api.Annotation, actor string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.Empty() {
		delete(s.annotations, key)
	} else {
		s.annotations[key] = a
	}
	s.appendAudit(api.AuditEntry{
		ID: newActionID(), Time: now, Actor: actor, Event: api.AuditMapEdited,
		Summary: annotationSummary(key, a), Outcome: api.ActionDone,
	})
	return nil
}

func (s *MemStore) Annotations() (map[string]api.Annotation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]api.Annotation, len(s.annotations))
	for k, v := range s.annotations {
		out[k] = v
	}
	return out, nil
}

func (s *MemStore) RecordLogin(user, ip string, ok bool, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendAudit(loginAuditEntry(user, ip, ok, now))
}

func (s *MemStore) CreateUser(username, hash, role, actor string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.users[username]; exists {
		return ErrUserExists
	}
	s.users[username] = localUserRec{hash: hash, info: api.LocalUser{
		Username: username, Role: role, CreatedBy: actor, CreatedAt: now,
	}}
	s.appendAudit(api.AuditEntry{
		ID: newActionID(), Time: now, Actor: actor, Event: api.AuditUser,
		Summary: fmt.Sprintf("creó el usuario %q (rol %s)", username, role), Outcome: "ok",
	})
	return nil
}

func (s *MemStore) DeleteUser(username, actor string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.users[username]; !exists {
		return ErrUnknownUser
	}
	delete(s.users, username)
	s.appendAudit(api.AuditEntry{
		ID: newActionID(), Time: now, Actor: actor, Event: api.AuditUser,
		Summary: fmt.Sprintf("eliminó el usuario %q", username), Outcome: "ok",
	})
	return nil
}

func (s *MemStore) ListUsers() ([]api.LocalUser, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]api.LocalUser, 0, len(s.users))
	for _, u := range s.users {
		out = append(out, u.info)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Username < out[b].Username })
	return out, nil
}

func (s *MemStore) UserAuth(username string) (string, string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[username]
	if !ok {
		return "", "", false
	}
	return u.hash, u.info.Role, true
}

func (s *MemStore) ListAudit(limit int) ([]api.AuditEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := len(s.audit)
	if limit <= 0 || limit > n {
		limit = n
	}
	// Devuelve las últimas 'limit', más recientes primero.
	out := make([]api.AuditEntry, 0, limit)
	for i := n - 1; i >= n-limit; i-- {
		out = append(out, s.audit[i])
	}
	return out, nil
}
