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
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
