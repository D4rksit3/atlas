package controlplane

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/atlasctl/atlas/pkg/api"
)

// Server expone la API HTTP del control plane. Dos superficies:
//   - agente -> control plane: /v1/agents/*  (el agente siempre inicia)
//   - GUI    -> control plane: /v1/topology
type Server struct {
	store             Store
	heartbeatInterval int
	metrics           *Metrics
	corsOrigin        string
}

// NewServer construye el servidor. heartbeatInterval son los segundos que se
// le indican al agente entre latidos. corsOrigin es el origen permitido para la
// GUI ("*" en desarrollo; restríngelo en producción).
func NewServer(store Store, heartbeatInterval int, corsOrigin string) *Server {
	if corsOrigin == "" {
		corsOrigin = "*"
	}
	return &Server{
		store:             store,
		heartbeatInterval: heartbeatInterval,
		metrics:           NewMetrics(),
		corsOrigin:        corsOrigin,
	}
}

// Routes devuelve el handler HTTP completo, ya con middlewares.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	// Patrones con método y comodín: requiere Go 1.22+.
	mux.HandleFunc("GET /healthz", s.handleHealth)  // liveness
	mux.HandleFunc("GET /readyz", s.handleHealth)   // readiness
	mux.HandleFunc("GET /metrics", s.handleMetrics) // Prometheus
	mux.HandleFunc("POST /v1/agents/register", s.handleRegister)
	mux.HandleFunc("POST /v1/agents/{id}/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("GET /v1/topology", s.handleTopology)
	// Acciones: la GUI encola, el agente ejecuta.
	mux.HandleFunc("POST /v1/clusters/{id}/actions", s.handleEnqueueAction)
	mux.HandleFunc("GET /v1/clusters/{id}/actions", s.handleListActions)
	return withCORS(s.corsOrigin, s.withObservability(mux))
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.metrics.WriteProm(w, s.store)
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req api.RegisterRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.ClusterID == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "clusterId y name son obligatorios")
		return
	}
	token, err := s.store.Register(req, time.Now())
	if err != nil {
		log.Printf("error registrando %q: %v", req.ClusterID, err)
		writeError(w, http.StatusInternalServerError, "no se pudo registrar")
		return
	}
	s.metrics.Registers.Add(1)
	log.Printf("registrado clúster %q (%s) provider=%s", req.Name, req.ClusterID, req.Provider)
	writeJSON(w, http.StatusOK, api.RegisterResponse{
		Token:                    token,
		HeartbeatIntervalSeconds: s.heartbeatInterval,
	})
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var hb api.Heartbeat
	if !readJSON(w, r, &hb) {
		return
	}
	now := time.Now()
	err := s.store.Heartbeat(id, hb.Token, hb.Snapshot, now)
	switch {
	case errors.Is(err, ErrUnknownCluster):
		s.metrics.HeartbeatErrors.Add(1)
		writeError(w, http.StatusNotFound, "clúster no registrado: vuelve a registrarte")
		return
	case errors.Is(err, ErrBadToken):
		s.metrics.HeartbeatErrors.Add(1)
		writeError(w, http.StatusUnauthorized, "token inválido")
		return
	case err != nil:
		s.metrics.HeartbeatErrors.Add(1)
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	s.metrics.Heartbeats.Add(1)

	// El agente reporta los resultados de las acciones que ejecutó; registramos.
	if err := s.store.RecordResults(id, hb.Results, now); err != nil {
		log.Printf("registrando resultados de acciones de %q: %v", id, err)
	}
	// Y le entregamos las acciones pendientes en la respuesta (viaje de vuelta).
	actions, err := s.store.TakeActions(id, now)
	if err != nil {
		log.Printf("recogiendo acciones de %q: %v", id, err)
	}
	writeJSON(w, http.StatusOK, api.HeartbeatResponse{Actions: actions})
}

func (s *Server) handleEnqueueAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req api.ActionRequest
	if !readJSON(w, r, &req) {
		return
	}
	action, err := s.store.EnqueueAction(id, req, time.Now())
	switch {
	case errors.Is(err, ErrUnknownCluster):
		writeError(w, http.StatusNotFound, "clúster desconocido")
	case errors.Is(err, ErrBadAction):
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		log.Printf("encolando acción en %q: %v", id, err)
		writeError(w, http.StatusInternalServerError, "no se pudo encolar la acción")
	default:
		s.metrics.Actions.Add(1)
		log.Printf("acción encolada en %q: %s %s/%s", id, action.Kind, action.Namespace, action.Workload)
		writeJSON(w, http.StatusAccepted, action)
	}
}

func (s *Server) handleListActions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	actions, err := s.store.ListActions(id)
	if errors.Is(err, ErrUnknownCluster) {
		writeError(w, http.StatusNotFound, "clúster desconocido")
		return
	}
	if err != nil {
		log.Printf("listando acciones de %q: %v", id, err)
		writeError(w, http.StatusInternalServerError, "no se pudieron leer las acciones")
		return
	}
	if actions == nil {
		actions = []api.Action{}
	}
	writeJSON(w, http.StatusOK, actions)
}

func (s *Server) handleTopology(w http.ResponseWriter, _ *http.Request) {
	topo, err := s.store.Topology(time.Now())
	if err != nil {
		log.Printf("error leyendo topología: %v", err)
		writeError(w, http.StatusInternalServerError, "no se pudo leer la topología")
		return
	}
	writeJSON(w, http.StatusOK, topo)
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("error escribiendo JSON: %v", err)
	}
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// readJSON decodifica el cuerpo; si falla, responde 400 y devuelve false.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)) // 1 MiB
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "JSON inválido: "+err.Error())
		return false
	}
	return true
}

// withCORS permite que la GUI consuma la API. En desarrollo origin="*"; en
// producción pásale el origen concreto de tu GUI.
func withCORS(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withObservability cuenta cada petición y registra método, ruta y latencia.
func (s *Server) withObservability(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		s.metrics.Requests.Add(1)
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}
