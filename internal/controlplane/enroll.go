// Vinculación por token: enlazar un clúster NUEVO con UN comando.
//
//	1. En la GUI (rol operator): "+ Vincular clúster" → POST /v1/enroll
//	   → token de UN SOLO USO que caduca en 15 min.
//	2. En el clúster nuevo:
//	   curl -sf https://atlas.tu-dominio/v1/enroll/TOKEN | kubectl apply -f -
//	   El GET canjea el token (lo QUEMA), emite un certificado mTLS al vuelo
//	   firmado por la CA de Atlas, y devuelve un manifiesto AUTOCONTENIDO
//	   (namespace + RBAC + Secret con el cert + Deployment del agente).
//
// Trade-off (elegido conscientemente): la CA se monta en el control plane para
// poder emitir al vuelo. Lo mitigan el token de un solo uso con TTL corto, la
// auditoría, las hojas cortas (90 días) y la CRL. El flujo estricto con la CA
// offline (atlas-certs) sigue disponible.
package controlplane

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/atlasctl/atlas/internal/auth"
	"github.com/atlasctl/atlas/internal/pki"
	"github.com/atlasctl/atlas/pkg/api"
)

// EnrollConfig configura el enrolamiento por token.
type EnrollConfig struct {
	CA         *pki.CA // CA de Atlas montada en el control plane (nil = deshabilitado)
	PublicURL  string  // URL mTLS a la que marcará el agente remoto (https://atlas-cp...:8443)
	AgentImage string  // imagen del agente para el manifiesto remoto
	CertDays   int     // vida de la hoja emitida (0 = 90)
}

// SetEnroll activa la vinculación por token en el servidor.
func (s *Server) SetEnroll(cfg EnrollConfig) { s.enroll = &cfg }

// registra las rutas de enrolamiento (llamado desde Routes).
func (s *Server) enrollRoutes(mux *http.ServeMux) {
	// Crear token: solo operadores desde la GUI.
	mux.Handle("POST /v1/enroll", s.guard(auth.RoleOperator, s.handleCreateEnroll))
	// Canjear token: SIN sesión — el token ES la credencial (un solo uso, TTL
	// corto, rate limit estricto y auditado).
	mux.HandleFunc("GET /v1/enroll/{token}", s.handleRedeemEnroll)
}

func (s *Server) handleCreateEnroll(w http.ResponseWriter, r *http.Request) {
	if s.enroll == nil || s.enroll.CA == nil {
		writeError(w, http.StatusBadRequest,
			"la vinculación por token no está activa: monta la CA en el control plane (--ca-cert/--ca-key) y define --agent-public-url")
		return
	}
	var req api.EnrollRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "hace falta un nombre para el clúster nuevo")
		return
	}
	if req.Provider == "" {
		req.Provider = api.ProviderOnPrem
	}
	actor := "dev"
	if u, ok := auth.UserFrom(r.Context()); ok {
		actor = u.Email
	}
	et, err := s.store.CreateEnrollToken(req.Name, req.Provider, actor, time.Now())
	if err != nil {
		log.Printf("creando token de vinculación: %v", err)
		writeError(w, http.StatusInternalServerError, "no se pudo crear el token")
		return
	}
	log.Printf("token de vinculación creado por %q para %q (caduca %s)", actor, req.Name, et.ExpiresAt.Format(time.RFC3339))
	writeJSON(w, http.StatusCreated, et)
}

func (s *Server) handleRedeemEnroll(w http.ResponseWriter, r *http.Request) {
	if s.enroll == nil || s.enroll.CA == nil {
		writeError(w, http.StatusNotFound, "la vinculación por token no está activa")
		return
	}
	// Anti fuerza bruta: mismo límite estricto que el login.
	if s.loginLimiter != nil && !s.loginLimiter.allow(clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "demasiados intentos; espera un momento")
		return
	}
	token := r.PathValue("token")
	et, err := s.store.ConsumeEnrollToken(token, time.Now())
	if err != nil {
		if errors.Is(err, ErrBadEnrollToken) {
			writeError(w, http.StatusNotFound, "token de vinculación inválido, caducado o ya usado")
			return
		}
		log.Printf("canjeando token de vinculación: %v", err)
		writeError(w, http.StatusInternalServerError, "error interno")
		return
	}
	// Emitir el certificado del agente AL VUELO (hoja corta firmada por la CA).
	certName := "agent-" + sanitizeName(et.Name)
	certPEM, keyPEM, err := s.enroll.CA.IssueClient(certName, s.enroll.CertDays)
	if err != nil {
		log.Printf("emitiendo certificado para %q: %v", et.Name, err)
		writeError(w, http.StatusInternalServerError, "no se pudo emitir el certificado")
		return
	}
	log.Printf("certificado %q emitido por vinculación (clúster %q)", certName, et.Name)
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, renderAgentManifest(et, s.enroll, certPEM, keyPEM))
}

// sanitizeName deja el nombre apto para K8s (minúsculas, dígitos, guiones).
func sanitizeName(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		switch {
		case c >= 'a' && c <= 'z' || c >= '0' && c <= '9':
			b.WriteRune(c)
		case c == '-' || c == ' ' || c == '_' || c == '.':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "cluster"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// renderAgentManifest genera el manifiesto AUTOCONTENIDO del agente remoto:
// namespace, RBAC de solo-lo-necesario, Secret con el cert mTLS recién emitido
// y el Deployment apuntando al control plane público.
func renderAgentManifest(et api.EnrollToken, cfg *EnrollConfig, certPEM, keyPEM []byte) string {
	id := sanitizeName(et.Name)
	indent := func(pem []byte) string {
		return "    " + strings.ReplaceAll(strings.TrimRight(string(pem), "\n"), "\n", "\n    ")
	}
	return fmt.Sprintf(`# Agente de Atlas para el clúster %q — generado por vinculación por token.
# Aplica este fichero UNA vez: kubectl apply -f -
# El certificado mTLS incluido es de VIDA CORTA y exclusivo de este clúster.
apiVersion: v1
kind: Namespace
metadata:
  name: atlas-system
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: atlas-agent
  namespace: atlas-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: atlas-agent-readonly
rules:
  - apiGroups: [""]
    resources: ["nodes", "pods", "services"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["pods/log", "events"]
    verbs: ["get", "list"]
  - apiGroups: [""]
    resources: ["nodes"]
    verbs: ["patch", "update"]
  - apiGroups: [""]
    resources: ["pods/eviction"]
    verbs: ["create"]
  - apiGroups: [""]
    resources: ["namespaces", "resourcequotas"]
    verbs: ["get", "list", "create", "update"]
  - apiGroups: ["apps"]
    resources: ["deployments", "statefulsets", "daemonsets"]
    verbs: ["get", "list", "watch", "update", "patch"]
  - apiGroups: ["networking.k8s.io"]
    resources: ["ingresses"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["argoproj.io"]
    resources: ["applications"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["cert-manager.io"]
    resources: ["clusterissuers"]
    verbs: ["get", "list", "watch", "create", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: atlas-agent-readonly
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: atlas-agent-readonly
subjects:
  - kind: ServiceAccount
    name: atlas-agent
    namespace: atlas-system
---
apiVersion: v1
kind: Secret
metadata:
  name: atlas-agent-mtls
  namespace: atlas-system
type: Opaque
stringData:
  tls.crt: |
%s
  tls.key: |
%s
  ca.crt: |
%s
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: atlas-agent
  namespace: atlas-system
  labels: { app: atlas-agent }
spec:
  replicas: 1
  selector:
    matchLabels: { app: atlas-agent }
  template:
    metadata:
      labels: { app: atlas-agent }
    spec:
      serviceAccountName: atlas-agent
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        seccompProfile: { type: RuntimeDefault }
      containers:
        - name: agent
          image: %s
          args:
            - "--collector=kube"
            - "--transport=grpc"
            - "--name=%s"
            - "--cluster-id=%s"
            - "--provider=%s"
          env:
            - name: ATLAS_CONTROL_PLANE
              value: "%s"
            - name: ATLAS_TLS_CERT
              value: /etc/atlas/tls/tls.crt
            - name: ATLAS_TLS_KEY
              value: /etc/atlas/tls/tls.key
            - name: ATLAS_TLS_CA
              value: /etc/atlas/tls/ca.crt
            - name: HELM_CACHE_HOME
              value: /helm/cache
            - name: HELM_CONFIG_HOME
              value: /helm/config
            - name: HELM_DATA_HOME
              value: /helm/data
          volumeMounts:
            - { name: mtls, mountPath: /etc/atlas/tls, readOnly: true }
            - { name: helm-cache, mountPath: /helm }
          resources:
            requests: { cpu: "25m", memory: "64Mi" }
            limits:   { cpu: "500m", memory: "512Mi" }
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities: { drop: ["ALL"] }
      volumes:
        - { name: mtls, secret: { secretName: atlas-agent-mtls } }
        - { name: helm-cache, emptyDir: {} }
`, et.Name,
		indent(certPEM), indent(keyPEM), indent(cfg.CA.CertPEM()),
		cfg.AgentImage, et.Name, id, et.Provider, cfg.PublicURL)
}
