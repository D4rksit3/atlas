package controlplane

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/atlasctl/atlas/pkg/api"
)

// Errores de dominio que el servidor traduce a códigos HTTP.
var (
	ErrUnknownCluster = errors.New("clúster desconocido")
	ErrBadToken       = errors.New("token inválido")
)

// Store es el registro de clústeres. Tiene dos implementaciones intercambiables:
//   - MemStore: en memoria (por defecto; se pierde al reiniciar, una sola réplica).
//   - PgStore:  Postgres (persistente y compartible entre varias réplicas).
//
// Las operaciones pueden fallar (Postgres puede caerse), por eso devuelven error.
type Store interface {
	// Register da de alta (o re-registra) un clúster y devuelve su token de sesión.
	// Conserva el último snapshot conocido para no "parpadear" en la GUI.
	Register(req api.RegisterRequest, now time.Time) (string, error)
	// Heartbeat actualiza el snapshot de un clúster tras validar su token.
	// Devuelve ErrUnknownCluster o ErrBadToken según el caso.
	Heartbeat(clusterID, token string, snap api.Snapshot, now time.Time) error
	// Topology devuelve la vista agregada, marcando offline lo que lleve demasiado
	// tiempo sin latir.
	Topology(now time.Time) (api.Topology, error)

	// EnqueueAction encola una acción para un clúster (devuelve ErrUnknownCluster
	// si no existe). actor es el usuario que la pide (para auditoría). La acción
	// queda 'pending' hasta que un latido la recoja.
	EnqueueAction(clusterID string, req api.ActionRequest, actor string, now time.Time) (api.Action, error)
	// TakeActions devuelve las acciones pendientes de un clúster y las marca como
	// 'dispatched' (entregadas). El agente las recibe en la respuesta del latido.
	TakeActions(clusterID string, now time.Time) ([]api.Action, error)
	// RecordResults actualiza el estado de las acciones según lo que reportó el
	// agente (done/error) y deja constancia en la auditoría.
	RecordResults(clusterID string, results []api.ActionResult, now time.Time) error
	// ListActions devuelve el historial reciente de acciones de un clúster (para
	// que la GUI muestre su estado).
	ListActions(clusterID string) ([]api.Action, error)
	// ListAudit devuelve las últimas entradas de auditoría (más recientes primero).
	ListAudit(limit int) ([]api.AuditEntry, error)

	// SetAnnotation guarda (o borra, si Empty) el metadato de una entidad del mapa.
	// actor es quién lo edita (para auditoría).
	SetAnnotation(key string, a api.Annotation, actor string, now time.Time) error
	// Annotations devuelve todas las anotaciones por clave, para que la GUI las
	// superponga al mapa.
	Annotations() (map[string]api.Annotation, error)

	// RecordLogin deja en la auditoría un intento de login local (bueno o malo)
	// con la IP de origen. Best-effort: nunca bloquea el login.
	RecordLogin(user, ip string, ok bool, now time.Time)

	// Usuarios locales adicionales (creados desde la GUI). El hash es bcrypt.
	CreateUser(username, hash, role, actor string, now time.Time) error
	DeleteUser(username, actor string, now time.Time) error
	ListUsers() ([]api.LocalUser, error)
	// UserAuth devuelve el hash y rol de un usuario para el login (ok=false si
	// no existe). No audita: eso lo hace RecordLogin.
	UserAuth(username string) (hash, role string, ok bool)

	// CreateEnrollToken emite un token de vinculación de un solo uso (caduca en
	// EnrollTTL). En reposo se guarda su hash; el token en claro solo sale aquí.
	// Queda auditado con el actor que lo pidió.
	CreateEnrollToken(name string, provider api.Provider, actor string, now time.Time) (api.EnrollToken, error)
	// ConsumeEnrollToken valida y QUEMA un token (un solo uso): devuelve sus
	// datos si era válido, o ErrBadEnrollToken si no existe, caducó o ya se usó.
	ConsumeEnrollToken(token string, now time.Time) (api.EnrollToken, error)
}

// EnrollTTL es la vida de un token de vinculación.
const EnrollTTL = 15 * time.Minute

// ErrBadEnrollToken cubre token inexistente, caducado o ya usado — a propósito
// indistinguibles para quien lo presenta.
var ErrBadEnrollToken = errors.New("token de vinculación inválido, caducado o ya usado")

// hashEnrollToken es la forma en reposo del token (que la DB no guarde nada
// directamente canjeable).
func hashEnrollToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ErrUserExists / ErrUnknownUser: altas duplicadas y bajas de inexistentes.
var (
	ErrUserExists  = errors.New("ese usuario ya existe")
	ErrUnknownUser = errors.New("usuario desconocido")
)

// annotationSummary describe una edición del mapa para la auditoría.
func annotationSummary(key string, a api.Annotation) string {
	if a.Empty() {
		return fmt.Sprintf("quitó los metadatos de %s", key)
	}
	if a.DisplayName != "" {
		return fmt.Sprintf("renombró %s a %q", key, a.DisplayName)
	}
	return fmt.Sprintf("editó los metadatos de %s", key)
}

// summarize describe una acción en lenguaje humano para la auditoría.
func summarize(a api.Action) string {
	switch a.Kind {
	case api.ActionScale:
		return fmt.Sprintf("escalar %s/%s a %d réplicas", a.Namespace, a.Workload, a.Replicas)
	case api.ActionRestart:
		return fmt.Sprintf("reiniciar %s/%s", a.Namespace, a.Workload)
	case api.ActionInstall:
		return fmt.Sprintf("instalar el complemento %q", a.Addon)
	case api.ActionAddApp:
		if a.App != nil {
			return fmt.Sprintf("registrar proyecto GitOps %q (%s)", a.App.Name, a.App.RepoURL)
		}
		return "registrar proyecto GitOps"
	case api.ActionIssuer:
		if a.Issuer != nil {
			return fmt.Sprintf("crear emisor TLS %q (%s)", a.Issuer.IssuerName(), a.Issuer.Environment)
		}
		return "crear emisor TLS"
	case api.ActionSync:
		if a.App != nil {
			return fmt.Sprintf("sincronizar proyecto %q", a.App.Name)
		}
		return "sincronizar proyecto"
	case api.ActionRollback:
		if a.App != nil {
			return fmt.Sprintf("revertir proyecto %q a la versión anterior", a.App.Name)
		}
		return "revertir proyecto"
	case api.ActionExpose:
		if a.Expose != nil {
			return fmt.Sprintf("publicar el servicio %s/%s en %s", a.Expose.Namespace, a.Expose.Service, a.Expose.Host)
		}
		return "publicar un servicio"
	case api.ActionUninstall:
		return fmt.Sprintf("desinstalar el complemento %q", a.Addon)
	case api.ActionLogs:
		return fmt.Sprintf("ver logs de %s/%s", a.Namespace, a.Workload)
	case api.ActionEvents:
		return fmt.Sprintf("ver eventos de %s", a.Namespace)
	case api.ActionCordon:
		return fmt.Sprintf("acordonar el nodo %s (no aceptará pods nuevos)", a.Node)
	case api.ActionUncordon:
		return fmt.Sprintf("reabrir el nodo %s", a.Node)
	case api.ActionDrain:
		return fmt.Sprintf("vaciar el nodo %s (mantenimiento)", a.Node)
	case api.ActionCreateNS:
		if a.NS != nil {
			return fmt.Sprintf("crear el namespace %q (cuotas: cpu=%s mem=%s)", a.NS.Name, orDash(a.NS.CPU), orDash(a.NS.Memory))
		}
		return "crear un namespace"
	case api.ActionUnexpose:
		if a.Expose != nil {
			return fmt.Sprintf("despublicar el servicio %s/%s", a.Expose.Namespace, a.Expose.Service)
		}
		return "despublicar un servicio"
	default:
		return a.Kind + " " + a.Namespace + "/" + a.Workload
	}
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newActionID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// validActionRequest valida una acción antes de encolarla.
func validActionRequest(req api.ActionRequest) error {
	switch req.Kind {
	case api.ActionInstall:
		// Instalar un complemento: solo hace falta el addon (el agente valida que
		// esté en su catálogo vetado).
		if req.Addon == "" {
			return errors.New("install requiere 'addon'")
		}
		return nil
	case api.ActionAddApp:
		if req.App == nil || req.App.Name == "" || req.App.RepoURL == "" || req.App.Namespace == "" {
			return errors.New("addapp requiere app.name, app.repoURL y app.namespace")
		}
		return nil
	case api.ActionIssuer:
		if req.Issuer == nil {
			return errors.New("issuer requiere el objeto 'issuer'")
		}
		if !strings.Contains(req.Issuer.Email, "@") {
			return errors.New("issuer requiere un email válido (cuenta ACME)")
		}
		if _, ok := api.ACMEDirectory(req.Issuer.Environment); !ok {
			return errors.New("issuer.environment debe ser 'staging' o 'production'")
		}
		return nil
	case api.ActionSync, api.ActionRollback:
		if req.App == nil || req.App.Name == "" {
			return errors.New("sync/rollback requieren app.name")
		}
		return nil
	case api.ActionExpose:
		e := req.Expose
		if e == nil || e.Namespace == "" || e.Service == "" || e.Host == "" {
			return errors.New("expose requiere expose.namespace, expose.service y expose.host")
		}
		if e.Port < 1 || e.Port > 65535 {
			return errors.New("expose.port fuera de rango (1..65535)")
		}
		if !validHost(e.Host) {
			return errors.New("expose.host no es un dominio válido")
		}
		return nil
	case api.ActionUninstall:
		if req.Addon == "" {
			return errors.New("uninstall requiere 'addon'")
		}
		return nil
	case api.ActionUnexpose:
		if req.Expose == nil || req.Expose.Namespace == "" || req.Expose.Service == "" {
			return errors.New("unexpose requiere expose.namespace y expose.service")
		}
		return nil
	case api.ActionLogs:
		if req.Namespace == "" || req.Workload == "" {
			return errors.New("logs requiere namespace y workload")
		}
		return nil
	case api.ActionEvents:
		if req.Namespace == "" {
			return errors.New("events requiere namespace")
		}
		return nil
	case api.ActionCordon, api.ActionUncordon, api.ActionDrain:
		if req.Node == "" {
			return errors.New(req.Kind + " requiere 'node'")
		}
		return nil
	case api.ActionCreateNS:
		if req.NS == nil || req.NS.Name == "" {
			return errors.New("createns requiere ns.name")
		}
		if !validHost(req.NS.Name) || strings.Contains(req.NS.Name, ".") {
			return errors.New("ns.name debe ser un nombre DNS válido (minúsculas, dígitos, guiones)")
		}
		return nil
	case api.ActionScale, api.ActionRestart:
		if req.Namespace == "" || req.Workload == "" {
			return errors.New("namespace y workload son obligatorios")
		}
		if req.WorkloadKind != "Deployment" && req.WorkloadKind != "StatefulSet" {
			return errors.New("workloadKind debe ser Deployment o StatefulSet")
		}
		if req.Kind == api.ActionScale && (req.Replicas < 0 || req.Replicas > 1000) {
			return errors.New("replicas fuera de rango (0..1000)")
		}
		return nil
	default:
		return errors.New("kind no soportado (usa: scale | restart | install | addapp | sync | rollback | issuer | expose)")
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// validHost acepta nombres DNS razonables (letras/dígitos/guiones por etiqueta,
// separadas por puntos). Corta hosts vacíos, con espacios o con caracteres raros.
func validHost(h string) bool {
	if len(h) == 0 || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i, c := range label {
			ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
				c == '-' && i > 0 && i < len(label)-1
			if !ok {
				return false
			}
		}
	}
	return true
}

// loginAuditEntry construye la entrada de auditoría de un intento de login.
func loginAuditEntry(user, ip string, ok bool, now time.Time) api.AuditEntry {
	e := api.AuditEntry{
		ID: newActionID(), Time: now, Actor: user, Event: api.AuditLogin,
		Summary: fmt.Sprintf("inicio de sesión desde %s", ip), Outcome: "ok",
	}
	if !ok {
		e.Summary = fmt.Sprintf("intento de login FALLIDO desde %s", ip)
		e.Outcome = "error"
	}
	return e
}

// ErrBadAction lo devuelve el store si la petición de acción es inválida.
var ErrBadAction = errors.New("acción inválida")
