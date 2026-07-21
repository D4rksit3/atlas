package controlplane

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
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
	// si no existe). La acción queda 'pending' hasta que un latido la recoja.
	EnqueueAction(clusterID string, req api.ActionRequest, now time.Time) (api.Action, error)
	// TakeActions devuelve las acciones pendientes de un clúster y las marca como
	// 'dispatched' (entregadas). El agente las recibe en la respuesta del latido.
	TakeActions(clusterID string, now time.Time) ([]api.Action, error)
	// RecordResults actualiza el estado de las acciones según lo que reportó el
	// agente (done/error).
	RecordResults(clusterID string, results []api.ActionResult, now time.Time) error
	// ListActions devuelve el historial reciente de acciones de un clúster (para
	// que la GUI muestre su estado).
	ListActions(clusterID string) ([]api.Action, error)
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
	if req.Namespace == "" || req.Workload == "" {
		return errors.New("namespace y workload son obligatorios")
	}
	switch req.Kind {
	case api.ActionScale:
		if req.Replicas < 0 || req.Replicas > 1000 {
			return errors.New("replicas fuera de rango (0..1000)")
		}
	case api.ActionRestart:
		// sin parámetros extra
	default:
		return errors.New("kind no soportado (usa: scale | restart)")
	}
	if req.WorkloadKind != "Deployment" && req.WorkloadKind != "StatefulSet" {
		return errors.New("workloadKind debe ser Deployment o StatefulSet")
	}
	return nil
}

// ErrBadAction lo devuelve el store si la petición de acción es inválida.
var ErrBadAction = errors.New("acción inválida")
