package controlplane

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/atlasctl/atlas/internal/auth"
	"github.com/atlasctl/atlas/pkg/api"
)

// Server expone la API HTTP del control plane. Dos superficies:
//   - agente -> control plane: /v1/agents/*  (autenticado por mTLS)
//   - GUI    -> control plane: /v1/topology, /v1/clusters/*  (autenticado por OIDC)
type Server struct {
	store             Store
	heartbeatInterval int
	metrics           *Metrics
	corsOrigin        string
	auth              *auth.Authenticator // nil = auth deshabilitada (desarrollo)
	limiter           *ipLimiter          // nil = sin rate limiting
	loginLimiter      *ipLimiter          // límite estricto de intentos de login por IP
	hub               *hub                // timbre GUI -> streams gRPC (empuje al instante)
}

// SetRateLimit configura el límite por IP (peticiones/segundo y ráfaga). perSec<=0
// lo desactiva.
func (s *Server) SetRateLimit(perSec float64, burst int) {
	if perSec <= 0 {
		s.limiter = nil
		return
	}
	s.limiter = newIPLimiter(perSec, burst)
}

// NewServer construye el servidor. heartbeatInterval son los segundos que se
// le indican al agente entre latidos. corsOrigin es el origen permitido para la
// GUI ("*" en desarrollo; restríngelo en producción). authn puede ser nil (sin
// autenticación, solo desarrollo).
func NewServer(store Store, heartbeatInterval int, corsOrigin string, authn *auth.Authenticator) *Server {
	if corsOrigin == "" {
		corsOrigin = "*"
	}
	return &Server{
		store:             store,
		heartbeatInterval: heartbeatInterval,
		metrics:           NewMetrics(),
		corsOrigin:        corsOrigin,
		auth:              authn,
		limiter:           newIPLimiter(20, 40),  // por defecto: 20 req/s por IP
		loginLimiter:      newIPLimiter(0.2, 5),  // login: 1 intento/5s por IP (ráfaga 5)
		hub:               newHub(),
	}
}

// Routes devuelve el handler HTTP completo, ya con middlewares.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	// Patrones con método y comodín: requiere Go 1.22+.
	mux.HandleFunc("GET /healthz", s.handleHealth)  // liveness
	mux.HandleFunc("GET /readyz", s.handleHealth)   // readiness
	mux.HandleFunc("GET /metrics", s.handleMetrics) // Prometheus
	// Endpoints del AGENTE: autenticados por mTLS, no por OIDC.
	mux.HandleFunc("POST /v1/agents/register", s.handleRegister)
	mux.HandleFunc("POST /v1/agents/{id}/heartbeat", s.handleHeartbeat)

	// Config pública para que la GUI sepa si debe pedir login y contra qué IdP.
	mux.HandleFunc("GET /v1/authconfig", s.handleAuthConfig)
	// Login local integrado (usuario/contraseña de Atlas). Rate limit propio y
	// estricto: bcrypt ya frena, pero además cortamos la fuerza bruta por IP.
	mux.HandleFunc("POST /v1/login", s.handleLogin)
	// Catálogo de complementos instalables (metadatos).
	mux.HandleFunc("GET /v1/addons", s.handleAddons)

	// Endpoints de la GUI: protegidos por OIDC + RBAC (si la auth está activa).
	//   leer topología / acciones -> viewer;  encolar acciones -> operator.
	mux.Handle("GET /v1/topology", s.guard(auth.RoleViewer, s.handleTopology))
	mux.Handle("GET /v1/clusters/{id}/actions", s.guard(auth.RoleViewer, s.handleListActions))
	mux.Handle("POST /v1/clusters/{id}/actions", s.guard(auth.RoleOperator, s.handleEnqueueAction))
	mux.Handle("GET /v1/audit", s.guard(auth.RoleViewer, s.handleAudit))
	// Editar el mapa (metadatos): leer -> viewer; escribir -> operator.
	mux.Handle("GET /v1/annotations", s.guard(auth.RoleViewer, s.handleListAnnotations))
	mux.Handle("PUT /v1/annotations/{key...}", s.guard(auth.RoleOperator, s.handleSetAnnotation))
	// Usuarios locales (equipo): gestionarlos exige rol operator.
	mux.Handle("GET /v1/users", s.guard(auth.RoleOperator, s.handleListUsers))
	mux.Handle("POST /v1/users", s.guard(auth.RoleOperator, s.handleCreateUser))
	mux.Handle("DELETE /v1/users/{name}", s.guard(auth.RoleOperator, s.handleDeleteUser))
	// Orden: cabeceras de seguridad -> CORS -> rate limit -> observabilidad -> rutas.
	return withSecurityHeaders(withCORS(s.corsOrigin, s.withRateLimit(s.withObservability(mux))))
}

// guard envuelve un handler con la comprobación de rol si la auth está activa;
// sin auth (desarrollo) lo deja pasar tal cual.
func (s *Server) guard(minRole string, h http.HandlerFunc) http.Handler {
	if s.auth == nil {
		return h
	}
	return s.auth.Require(minRole, h)
}

func (s *Server) handleAddons(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, api.Addons())
}

func (s *Server) handleAuthConfig(w http.ResponseWriter, _ *http.Request) {
	if s.auth == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, s.auth.PublicConfig())
}

// handleLogin canjea usuario/contraseña del login local por un token de sesión.
// Cada intento (bueno o malo) queda en la auditoría con la IP de origen.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil || !s.auth.HasLocal() {
		writeError(w, http.StatusNotFound, "el login local no está configurado")
		return
	}
	ip := clientIP(r)
	if s.loginLimiter != nil && !s.loginLimiter.allow(ip) {
		writeError(w, http.StatusTooManyRequests, "demasiados intentos de login; espera un momento")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	token, exp, err := s.auth.Login(req.Username, req.Password)
	if err != nil {
		s.store.RecordLogin(req.Username, ip, false, time.Now())
		log.Printf("login FALLIDO de %q desde %s", req.Username, ip)
		writeError(w, http.StatusUnauthorized, "usuario o contraseña incorrectos")
		return
	}
	s.store.RecordLogin(req.Username, ip, true, time.Now())
	log.Printf("login correcto de %q desde %s", req.Username, ip)
	writeJSON(w, http.StatusOK, map[string]any{
		"token": token,
		"user":  req.Username,
		"exp":   exp.Unix(),
	})
}

// ---- usuarios locales (gestión del equipo desde la GUI) ----

func (s *Server) handleListUsers(w http.ResponseWriter, _ *http.Request) {
	users, err := s.store.ListUsers()
	if err != nil {
		log.Printf("listando usuarios: %v", err)
		writeError(w, http.StatusInternalServerError, "no se pudieron leer los usuarios")
		return
	}
	if users == nil {
		users = []api.LocalUser{}
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil || !s.auth.HasLocal() {
		writeError(w, http.StatusBadRequest, "el login local no está activo (los usuarios locales requieren ATLAS_ADMIN_PASSWORD)")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Username) < 2 || len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "usuario (≥2) y contraseña (≥8 caracteres) son obligatorios")
		return
	}
	if req.Role != auth.RoleOperator {
		req.Role = auth.RoleViewer
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "no se pudo procesar la contraseña")
		return
	}
	actor := "dev"
	if u, ok := auth.UserFrom(r.Context()); ok {
		actor = u.Email
	}
	if err := s.store.CreateUser(req.Username, hash, req.Role, actor, time.Now()); err != nil {
		if errors.Is(err, ErrUserExists) {
			writeError(w, http.StatusConflict, "ese usuario ya existe")
			return
		}
		log.Printf("creando usuario %q: %v", req.Username, err)
		writeError(w, http.StatusInternalServerError, "no se pudo crear el usuario")
		return
	}
	log.Printf("usuario %q (rol %s) creado por %q", req.Username, req.Role, actor)
	writeJSON(w, http.StatusCreated, api.LocalUser{Username: req.Username, Role: req.Role, CreatedBy: actor, CreatedAt: time.Now()})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	actor := "dev"
	if u, ok := auth.UserFrom(r.Context()); ok {
		actor = u.Email
	}
	if name == actor {
		writeError(w, http.StatusBadRequest, "no puedes borrar tu propio usuario en sesión")
		return
	}
	if err := s.store.DeleteUser(name, actor, time.Now()); err != nil {
		if errors.Is(err, ErrUnknownUser) {
			writeError(w, http.StatusNotFound, "usuario desconocido")
			return
		}
		log.Printf("borrando usuario %q: %v", name, err)
		writeError(w, http.StatusInternalServerError, "no se pudo borrar el usuario")
		return
	}
	log.Printf("usuario %q eliminado por %q", name, actor)
	writeJSON(w, http.StatusOK, map[string]string{"deleted": name})
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
	actor := "dev" // sin auth (desarrollo)
	if u, ok := auth.UserFrom(r.Context()); ok {
		actor = u.Email
	}
	action, err := s.store.EnqueueAction(id, req, actor, time.Now())
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
		// Si el agente está conectado por stream gRPC, esto le empuja la acción
		// AL INSTANTE; si va por HTTP, la recogerá en su próximo latido.
		s.hub.notify(id)
		log.Printf("acción encolada en %q por %q: %s %s/%s", id, actor, action.Kind, action.Namespace, action.Workload)
		writeJSON(w, http.StatusAccepted, action)
	}
}

func (s *Server) handleListAnnotations(w http.ResponseWriter, _ *http.Request) {
	annos, err := s.store.Annotations()
	if err != nil {
		log.Printf("leyendo anotaciones: %v", err)
		writeError(w, http.StatusInternalServerError, "no se pudieron leer las anotaciones")
		return
	}
	if annos == nil {
		annos = map[string]api.Annotation{}
	}
	writeJSON(w, http.StatusOK, annos)
}

func (s *Server) handleSetAnnotation(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "falta la clave de la entidad")
		return
	}
	var anno api.Annotation
	if !readJSON(w, r, &anno) {
		return
	}
	actor := "dev"
	if u, ok := auth.UserFrom(r.Context()); ok {
		actor = u.Email
	}
	if err := s.store.SetAnnotation(key, anno, actor, time.Now()); err != nil {
		log.Printf("guardando anotación %q: %v", key, err)
		writeError(w, http.StatusInternalServerError, "no se pudo guardar")
		return
	}
	log.Printf("mapa editado por %q: %s", actor, key)
	writeJSON(w, http.StatusOK, anno)
}

func (s *Server) handleAudit(w http.ResponseWriter, _ *http.Request) {
	entries, err := s.store.ListAudit(200)
	if err != nil {
		log.Printf("leyendo auditoría: %v", err)
		writeError(w, http.StatusInternalServerError, "no se pudo leer la auditoría")
		return
	}
	if entries == nil {
		entries = []api.AuditEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
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
	// Los values de un install pueden llevar SECRETOS (p. ej. la contraseña de
	// Grafana): se enmascaran antes de salir. La GUI usa el resto de valores
	// para precargar "Configurar" con lo realmente aplicado.
	for i := range actions {
		actions[i].Values = redactValues(actions[i].Addon, actions[i].Values)
	}
	writeJSON(w, http.StatusOK, actions)
}

// redactValues enmascara los valores de tipo password según el catálogo.
func redactValues(addon string, values map[string]string) map[string]string {
	if len(values) == 0 {
		return values
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		out[k] = v
	}
	for _, p := range api.AddonParams(addon) {
		if p.Type == "password" {
			if _, ok := out[p.Key]; ok {
				out[p.Key] = "••••••"
			}
		}
	}
	return out
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
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
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
