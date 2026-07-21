package controlplane

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
}

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
	case api.ActionSync, api.ActionRollback:
		if req.App == nil || req.App.Name == "" {
			return errors.New("sync/rollback requieren app.name")
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
		return errors.New("kind no soportado (usa: scale | restart | install)")
	}
}

// ErrBadAction lo devuelve el store si la petición de acción es inválida.
var ErrBadAction = errors.New("acción inválida")
