// Panel de Actividad: el registro de auditoría — quién hizo qué y cuándo. Hace
// poll a /v1/audit y muestra las entradas más recientes con su resultado.
import { useEffect, useState } from "react";
import { fetchAudit, type AuditEntry } from "./api";

const POLL_MS = 4000;

function ago(iso: string): string {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return `hace ${Math.floor(s)}s`;
  if (s < 3600) return `hace ${Math.floor(s / 60)}m`;
  if (s < 86400) return `hace ${Math.floor(s / 3600)}h`;
  return new Date(iso).toLocaleDateString();
}

function dot(outcome: string): string {
  if (outcome === "ok") return "var(--good)";
  if (outcome === "error") return "#ff7c7c";
  return "var(--faint)"; // pending
}

export function AuditPanel({ onClose }: { onClose: () => void }) {
  const [entries, setEntries] = useState<AuditEntry[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    const load = async () => {
      try {
        const e = await fetchAudit();
        if (alive) {
          setEntries(e);
          setErr(null);
        }
      } catch (e) {
        if (alive) setErr(String(e));
      }
    };
    load();
    const id = setInterval(load, POLL_MS);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, []);

  return (
    <aside className="audit">
      <div className="audit-head">
        <div className="audit-title">Actividad</div>
        <button className="insp-x" onClick={onClose} aria-label="cerrar">
          ×
        </button>
      </div>
      <div className="audit-body">
        {err && <div className="audit-empty err">sin acceso al registro</div>}
        {entries && entries.length === 0 && (
          <div className="audit-empty">todavía no hay actividad</div>
        )}
        {entries?.map((e) => (
          <div className="audit-row" key={e.id}>
            <span className="audit-dot" style={{ background: dot(e.outcome) }} />
            <div className="audit-main">
              <div className="audit-summary">{e.summary}</div>
              <div className="audit-meta">
                <span className="audit-actor">{e.actor || "—"}</span> ·{" "}
                {e.event === "action.requested" ? "solicitó" : "ejecutó"} · {ago(e.time)}
                {e.outcome === "error" && e.error && (
                  <span className="audit-err"> · {e.error}</span>
                )}
              </div>
            </div>
          </div>
        ))}
      </div>
    </aside>
  );
}
