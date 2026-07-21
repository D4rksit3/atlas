package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/atlasctl/atlas/pkg/api"
)

// PgStore persiste los clústeres en Postgres. A diferencia de MemStore, sobrevive
// a reinicios y lo pueden compartir varias réplicas del control plane (el estado
// vive en la base de datos, no en el proceso).
type PgStore struct {
	pool         *pgxpool.Pool
	offlineAfter time.Duration
}

const pgSchema = `
CREATE TABLE IF NOT EXISTS clusters (
    cluster_id    TEXT PRIMARY KEY,
    name          TEXT        NOT NULL,
    provider      TEXT        NOT NULL,
    token         TEXT        NOT NULL,
    agent_version TEXT        NOT NULL DEFAULT '',
    last_seen     TIMESTAMPTZ NOT NULL,
    snapshot      JSONB       NOT NULL DEFAULT '{}'::jsonb
);`

// NewPgStore conecta a Postgres (DSN estilo postgres://user:pass@host:port/db) y
// crea la tabla si no existe. offlineAfter es el umbral para marcar offline.
func NewPgStore(ctx context.Context, dsn string, offlineAfter time.Duration) (*PgStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("conectando a Postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("Postgres no responde: %w", err)
	}
	if _, err := pool.Exec(ctx, pgSchema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("creando esquema: %w", err)
	}
	return &PgStore{pool: pool, offlineAfter: offlineAfter}, nil
}

// Close libera el pool de conexiones.
func (s *PgStore) Close() { s.pool.Close() }

func (s *PgStore) Register(req api.RegisterRequest, now time.Time) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	token := newToken()
	emptySnap, _ := json.Marshal(api.Snapshot{})
	// En un re-registro NO tocamos snapshot (se conserva el último mapa conocido);
	// solo en el alta inicial se usa el snapshot vacío del VALUES.
	_, err := s.pool.Exec(ctx, `
		INSERT INTO clusters (cluster_id, name, provider, token, agent_version, last_seen, snapshot)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (cluster_id) DO UPDATE
		SET name = EXCLUDED.name, provider = EXCLUDED.provider, token = EXCLUDED.token,
		    agent_version = EXCLUDED.agent_version, last_seen = EXCLUDED.last_seen`,
		req.ClusterID, req.Name, string(req.Provider), token, req.AgentVersion, now, string(emptySnap))
	if err != nil {
		return "", fmt.Errorf("registrando clúster: %w", err)
	}
	return token, nil
}

func (s *PgStore) Heartbeat(clusterID, token string, snap api.Snapshot, now time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("serializando snapshot: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE clusters SET snapshot = $3, last_seen = $4
		WHERE cluster_id = $1 AND token = $2`,
		clusterID, token, string(raw), now)
	if err != nil {
		return fmt.Errorf("actualizando latido: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	// No se actualizó nada: ¿clúster inexistente o token equivocado?
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM clusters WHERE cluster_id = $1)`, clusterID).Scan(&exists); err != nil {
		return fmt.Errorf("comprobando clúster: %w", err)
	}
	if !exists {
		return ErrUnknownCluster
	}
	return ErrBadToken
}

func (s *PgStore) Topology(now time.Time) (api.Topology, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out := api.Topology{GeneratedAt: now, Clusters: []api.ClusterView{}}
	rows, err := s.pool.Query(ctx, `
		SELECT cluster_id, name, provider, agent_version, last_seen, snapshot
		FROM clusters ORDER BY cluster_id`)
	if err != nil {
		return out, fmt.Errorf("leyendo topología: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			v       api.ClusterView
			prov    string
			raw     []byte
			lastSee time.Time
		)
		if err := rows.Scan(&v.ClusterID, &v.Name, &prov, &v.AgentVersion, &lastSee, &raw); err != nil {
			return out, fmt.Errorf("escaneando clúster: %w", err)
		}
		v.Provider = api.Provider(prov)
		v.LastSeen = lastSee
		v.Online = now.Sub(lastSee) <= s.offlineAfter
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &v.Snapshot); err != nil {
				return out, fmt.Errorf("deserializando snapshot de %s: %w", v.ClusterID, err)
			}
		}
		out.Clusters = append(out.Clusters, v)
	}
	if err := rows.Err(); err != nil && err != pgx.ErrNoRows {
		return out, fmt.Errorf("iterando clústeres: %w", err)
	}
	return out, nil
}
