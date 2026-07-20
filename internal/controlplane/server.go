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
	store             *Store
	heartbeatInterval int
}

// NewServer construye el servidor. heartbeatInterval son los segundos que se
// le indican al agente entre latidos.
func NewServer(store *Store, heartbeatInterval int) *Server {
	return &Server{store: store, heartbeatInterval: heartbeatInterval}
}

// Routes devuelve el handler HTTP completo, ya con middlewares.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	// Patrones con método y comodín: requiere Go 1.22+.
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /v1/agents/register", s.handleRegister)
	mux.HandleFunc("POST /v1/agents/{id}/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("GET /v1/topology", s.handleTopology)
	return withCORS(withLogging(mux))
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
	token := s.store.Register(req, time.Now())
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
	err := s.store.Heartbeat(id, hb.Token, hb.Snapshot, time.Now())
	switch {
	case errors.Is(err, ErrUnknownCluster):
		writeError(w, http.StatusNotFound, "clúster no registrado: vuelve a registrarte")
	case errors.Is(err, ErrBadToken):
		writeError(w, http.StatusUnauthorized, "token inválido")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "error interno")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleTopology(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Topology(time.Now()))
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

// withCORS permite que la GUI (en otro origen durante el desarrollo) consuma
// la API. Ajusta el origen permitido antes de producción.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}
