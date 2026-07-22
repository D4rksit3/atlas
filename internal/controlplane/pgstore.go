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
);
CREATE TABLE IF NOT EXISTS actions (
    id            TEXT PRIMARY KEY,
    cluster_id    TEXT        NOT NULL REFERENCES clusters(cluster_id) ON DELETE CASCADE,
    kind          TEXT        NOT NULL,
    namespace     TEXT        NOT NULL,
    workload      TEXT        NOT NULL,
    workload_kind TEXT        NOT NULL,
    replicas      INT         NOT NULL DEFAULT 0,
    addon         TEXT        NOT NULL DEFAULT '',
    app_spec      JSONB,
    issuer_spec   JSONB,
    vals          JSONB,
    status        TEXT        NOT NULL,
    error         TEXT        NOT NULL DEFAULT '',
    requested_by  TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL
);
-- Migración idempotente para DBs creadas antes de la acción 'issuer'.
ALTER TABLE actions ADD COLUMN IF NOT EXISTS issuer_spec JSONB;
CREATE INDEX IF NOT EXISTS actions_cluster_status ON actions(cluster_id, status);
CREATE TABLE IF NOT EXISTS audit (
    id         TEXT PRIMARY KEY,
    ts         TIMESTAMPTZ NOT NULL,
    actor      TEXT        NOT NULL DEFAULT '',
    event      TEXT        NOT NULL,
    cluster_id TEXT        NOT NULL,
    namespace  TEXT        NOT NULL DEFAULT '',
    workload   TEXT        NOT NULL DEFAULT '',
    summary    TEXT        NOT NULL DEFAULT '',
    outcome    TEXT        NOT NULL DEFAULT '',
    error      TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS audit_ts ON audit(ts DESC);
CREATE TABLE IF NOT EXISTS annotations (
    key          TEXT PRIMARY KEY,
    display_name TEXT        NOT NULL DEFAULT '',
    color        TEXT        NOT NULL DEFAULT '',
    note         TEXT        NOT NULL DEFAULT '',
    updated_by   TEXT        NOT NULL DEFAULT '',
    updated_at   TIMESTAMPTZ NOT NULL
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

func (s *PgStore) EnqueueAction(clusterID string, req api.ActionRequest, actor string, now time.Time) (api.Action, error) {
	if err := validActionRequest(req); err != nil {
		return api.Action{}, fmt.Errorf("%w: %v", ErrBadAction, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM clusters WHERE cluster_id = $1)`, clusterID).Scan(&exists); err != nil {
		return api.Action{}, fmt.Errorf("comprobando clúster: %w", err)
	}
	if !exists {
		return api.Action{}, ErrUnknownCluster
	}
	a := api.Action{
		ID: newActionID(), Kind: req.Kind, Namespace: req.Namespace,
		Workload: req.Workload, WorkloadKind: req.WorkloadKind, Replicas: req.Replicas,
		Addon: req.Addon, Values: req.Values, App: req.App, Issuer: req.Issuer,
		Status: api.ActionPending, RequestedBy: actor, CreatedAt: now, UpdatedAt: now,
	}
	var appJSON, issuerJSON, valsJSON interface{}
	if a.App != nil {
		b, _ := json.Marshal(a.App)
		appJSON = string(b)
	}
	if a.Issuer != nil {
		b, _ := json.Marshal(a.Issuer)
		issuerJSON = string(b)
	}
	if len(a.Values) > 0 {
		b, _ := json.Marshal(a.Values)
		valsJSON = string(b)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO actions (id, cluster_id, kind, namespace, workload, workload_kind, replicas, addon, app_spec, issuer_spec, vals, status, requested_by, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14)`,
		a.ID, clusterID, a.Kind, a.Namespace, a.Workload, a.WorkloadKind, a.Replicas, a.Addon, appJSON, issuerJSON, valsJSON, a.Status, actor, now)
	if err != nil {
		return api.Action{}, fmt.Errorf("encolando acción: %w", err)
	}
	s.insertAudit(ctx, api.AuditEntry{
		ID: newActionID(), Time: now, Actor: actor, Event: api.AuditRequested,
		Cluster: clusterID, Namespace: a.Namespace, Workload: a.Workload,
		Summary: summarize(a), Outcome: api.ActionPending,
	})
	return a, nil
}

func (s *PgStore) insertAudit(ctx context.Context, e api.AuditEntry) {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO audit (id, ts, actor, event, cluster_id, namespace, workload, summary, outcome, error)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		e.ID, e.Time, e.Actor, e.Event, e.Cluster, e.Namespace, e.Workload, e.Summary, e.Outcome, e.Error)
	if err != nil {
		// La auditoría es best-effort: no rompas la operación si falla el insert.
		fmt.Printf("aviso: no pude escribir auditoría: %v\n", err)
	}
}

func (s *PgStore) TakeActions(clusterID string, now time.Time) ([]api.Action, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Marca como 'dispatched' las pendientes y las devuelve, en una sola sentencia.
	rows, err := s.pool.Query(ctx, `
		UPDATE actions SET status = $2, updated_at = $3
		WHERE cluster_id = $1 AND status = $4
		RETURNING id, kind, namespace, workload, workload_kind, replicas, addon, app_spec, issuer_spec, vals, status, error, created_at, updated_at`,
		clusterID, api.ActionDispatched, now, api.ActionPending)
	if err != nil {
		return nil, fmt.Errorf("recogiendo acciones: %w", err)
	}
	defer rows.Close()
	return scanActions(rows)
}

func (s *PgStore) RecordResults(clusterID string, results []api.ActionResult, now time.Time) error {
	if len(results) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, r := range results {
		status, errMsg, outcome := api.ActionDone, "", api.ActionDone
		if !r.OK {
			status, errMsg, outcome = api.ActionError, r.Error, api.ActionError
		}
		var a api.Action
		err := s.pool.QueryRow(ctx, `
			UPDATE actions SET status = $3, error = $4, updated_at = $5
			WHERE cluster_id = $1 AND id = $2
			RETURNING kind, namespace, workload, replicas, requested_by`,
			clusterID, r.ID, status, errMsg, now).Scan(
			&a.Kind, &a.Namespace, &a.Workload, &a.Replicas, &a.RequestedBy)
		if err == pgx.ErrNoRows {
			continue // resultado de una acción que ya no existe
		}
		if err != nil {
			return fmt.Errorf("registrando resultado de %s: %w", r.ID, err)
		}
		s.insertAudit(ctx, api.AuditEntry{
			ID: newActionID(), Time: now, Actor: a.RequestedBy, Event: api.AuditExecuted,
			Cluster: clusterID, Namespace: a.Namespace, Workload: a.Workload,
			Summary: summarize(a), Outcome: outcome, Error: errMsg,
		})
	}
	return nil
}

func (s *PgStore) SetAnnotation(key string, a api.Annotation, actor string, now time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if a.Empty() {
		if _, err := s.pool.Exec(ctx, `DELETE FROM annotations WHERE key = $1`, key); err != nil {
			return fmt.Errorf("borrando anotación: %w", err)
		}
	} else {
		_, err := s.pool.Exec(ctx, `
			INSERT INTO annotations (key, display_name, color, note, updated_by, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (key) DO UPDATE
			SET display_name = EXCLUDED.display_name, color = EXCLUDED.color,
			    note = EXCLUDED.note, updated_by = EXCLUDED.updated_by, updated_at = EXCLUDED.updated_at`,
			key, a.DisplayName, a.Color, a.Note, actor, now)
		if err != nil {
			return fmt.Errorf("guardando anotación: %w", err)
		}
	}
	s.insertAudit(ctx, api.AuditEntry{
		ID: newActionID(), Time: now, Actor: actor, Event: api.AuditMapEdited,
		Summary: annotationSummary(key, a), Outcome: api.ActionDone,
	})
	return nil
}

func (s *PgStore) Annotations() (map[string]api.Annotation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT key, display_name, color, note FROM annotations`)
	if err != nil {
		return nil, fmt.Errorf("leyendo anotaciones: %w", err)
	}
	defer rows.Close()
	out := make(map[string]api.Annotation)
	for rows.Next() {
		var k string
		var a api.Annotation
		if err := rows.Scan(&k, &a.DisplayName, &a.Color, &a.Note); err != nil {
			return nil, fmt.Errorf("escaneando anotación: %w", err)
		}
		out[k] = a
	}
	return out, rows.Err()
}

func (s *PgStore) RecordLogin(user, ip string, ok bool, now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.insertAudit(ctx, loginAuditEntry(user, ip, ok, now))
}

func (s *PgStore) ListAudit(limit int) ([]api.AuditEntry, error) {
	if limit <= 0 {
		limit = 200
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT id, ts, actor, event, cluster_id, namespace, workload, summary, outcome, error
		FROM audit ORDER BY ts DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("leyendo auditoría: %w", err)
	}
	defer rows.Close()
	var out []api.AuditEntry
	for rows.Next() {
		var e api.AuditEntry
		if err := rows.Scan(&e.ID, &e.Time, &e.Actor, &e.Event, &e.Cluster,
			&e.Namespace, &e.Workload, &e.Summary, &e.Outcome, &e.Error); err != nil {
			return nil, fmt.Errorf("escaneando auditoría: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *PgStore) ListActions(clusterID string) ([]api.Action, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT id, kind, namespace, workload, workload_kind, replicas, addon, app_spec, issuer_spec, vals, status, error, created_at, updated_at
		FROM actions WHERE cluster_id = $1 ORDER BY created_at`, clusterID)
	if err != nil {
		return nil, fmt.Errorf("listando acciones: %w", err)
	}
	defer rows.Close()
	return scanActions(rows)
}

func scanActions(rows pgx.Rows) ([]api.Action, error) {
	var out []api.Action
	for rows.Next() {
		var a api.Action
		var appJSON, issuerJSON, valsJSON []byte
		if err := rows.Scan(&a.ID, &a.Kind, &a.Namespace, &a.Workload, &a.WorkloadKind,
			&a.Replicas, &a.Addon, &appJSON, &issuerJSON, &valsJSON, &a.Status, &a.Error, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, fmt.Errorf("escaneando acción: %w", err)
		}
		if len(appJSON) > 0 {
			_ = json.Unmarshal(appJSON, &a.App)
		}
		if len(issuerJSON) > 0 {
			_ = json.Unmarshal(issuerJSON, &a.Issuer)
		}
		if len(valsJSON) > 0 {
			_ = json.Unmarshal(valsJSON, &a.Values)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
