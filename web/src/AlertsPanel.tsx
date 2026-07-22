// Panel de Alertas: lo que Atlas VIGILA por ti — clúster offline, nodos
// NotReady, pods en CrashLoop o sin imagen. Con ATLAS_ALERT_WEBHOOK además te
// avisa fuera de la GUI (webhook JSON) al aparecer y al resolverse.
import { useEffect, useState } from "react";
import { fetchAlerts, type Alert } from "./api";

const POLL_MS = 10000;

function ago(iso: string): string {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return `hace ${Math.floor(s)}s`;
  if (s < 3600) return `hace ${Math.floor(s / 60)}m`;
  return `hace ${Math.floor(s / 3600)}h`;
}

export function AlertsPanel({ onClose }: { onClose: () => void }) {
  const [alerts, setAlerts] = useState<Alert[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    const load = async () => {
      try {
        const a = await fetchAlerts();
        if (alive) {
          setAlerts(a);
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
    <aside className="audit alerts-panel">
      <div className="audit-head">
        <div className="audit-title">Alertas</div>
        <button className="insp-x" onClick={onClose} aria-label="cerrar">
          ×
        </button>
      </div>
      <div className="audit-body">
        {err && <div className="audit-empty err">sin acceso a las alertas</div>}
        {alerts && alerts.length === 0 && (
          <div className="audit-empty ok-empty">✓ todo en orden — sin alertas activas</div>
        )}
        {alerts?.map((a) => (
          <div className="alert-row" key={a.id}>
            <span
              className="audit-dot"
              style={{ background: a.severity === "critical" ? "#ff5c5c" : "#F0932B" }}
            />
            <div className="alert-main">
              <div className="alert-msg">{a.message}</div>
              <div className="alert-meta">
                {a.severity} · {a.cluster} · {ago(a.since)}
              </div>
            </div>
          </div>
        ))}
        <div className="insp-hint-sm alerts-hint">
          Para recibir avisos fuera de la GUI, configura{" "}
          <span className="mono">ATLAS_ALERT_WEBHOOK</span> en el control plane:
          recibe un POST JSON al aparecer y al resolverse cada alerta.
        </div>
      </div>
    </aside>
  );
}
