// Panel Inspector: al seleccionar una carga en el mapa, permite OPERARLA desde la
// GUI — escalar réplicas o reiniciar. La orden se encola en el control plane y el
// agente la ejecuta en el clúster (viaja de vuelta en el latido). Aquí seguimos
// el estado de la acción hasta que el agente la confirma.
import { useEffect, useRef, useState } from "react";
import { postAction, fetchActions, type ActionStatus } from "./api";
import type { WorkloadOp } from "./ServiceNode";

interface Feedback {
  text: string;
  tone: "info" | "ok" | "err";
}

export function Inspector({ op, onClose }: { op: WorkloadOp; onClose: () => void }) {
  const [replicas, setReplicas] = useState(op.replicas);
  const [busy, setBusy] = useState(false);
  const [fb, setFb] = useState<Feedback | null>(null);
  const poll = useRef<number | null>(null);

  // Al cambiar de carga seleccionada, resetea el formulario.
  useEffect(() => {
    setReplicas(op.replicas);
    setFb(null);
    return stopPoll;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [op.clusterId, op.namespace, op.workload]);

  function stopPoll() {
    if (poll.current) window.clearInterval(poll.current);
    poll.current = null;
  }

  // Sigue el estado de una acción hasta done/error (o se rinde tras ~40s).
  function track(id: string, verb: string) {
    stopPoll();
    let tries = 0;
    poll.current = window.setInterval(async () => {
      tries++;
      try {
        const actions = await fetchActions(op.clusterId);
        const a = actions.find((x) => x.id === id);
        const st = a?.status as ActionStatus | undefined;
        if (st === "done") {
          stopPoll();
          setBusy(false);
          setFb({ text: `${verb} aplicado ✓`, tone: "ok" });
        } else if (st === "error") {
          stopPoll();
          setBusy(false);
          setFb({ text: `error: ${a?.error ?? "desconocido"}`, tone: "err" });
        } else if (tries > 20) {
          stopPoll();
          setBusy(false);
          setFb({ text: `${verb}: sin confirmación aún (¿agente offline?)`, tone: "err" });
        }
      } catch {
        /* reintenta en el siguiente tick */
      }
    }, 2000);
  }

  async function scale() {
    setBusy(true);
    setFb({ text: "enviando orden de escalado…", tone: "info" });
    try {
      const a = await postAction(op.clusterId, {
        kind: "scale",
        namespace: op.namespace,
        workload: op.workload,
        workloadKind: op.workloadKind,
        replicas,
      });
      setFb({ text: `escalando a ${replicas}… (agente ejecutando)`, tone: "info" });
      track(a.id, `escalar a ${replicas}`);
    } catch (e) {
      setBusy(false);
      setFb({ text: `no se pudo encolar: ${String(e)}`, tone: "err" });
    }
  }

  async function restart() {
    setBusy(true);
    setFb({ text: "enviando orden de reinicio…", tone: "info" });
    try {
      const a = await postAction(op.clusterId, {
        kind: "restart",
        namespace: op.namespace,
        workload: op.workload,
        workloadKind: op.workloadKind,
        replicas: op.replicas,
      });
      setFb({ text: "reiniciando… (rollout en curso)", tone: "info" });
      track(a.id, "reinicio");
    } catch (e) {
      setBusy(false);
      setFb({ text: `no se pudo encolar: ${String(e)}`, tone: "err" });
    }
  }

  const canOperate = op.online;

  return (
    <aside className="inspector">
      <div className="insp-head">
        <div>
          <div className="insp-title">{op.workload}</div>
          <div className="insp-sub">
            {op.workloadKind} · {op.namespace}
          </div>
        </div>
        <button className="insp-x" onClick={onClose} aria-label="cerrar">
          ×
        </button>
      </div>

      <div className="insp-body">
        {!canOperate && (
          <div className="insp-note err">
            El clúster está offline: las acciones no se ejecutarán hasta que el
            agente vuelva a latir.
          </div>
        )}

        <label className="insp-label">Réplicas</label>
        <div className="insp-stepper">
          <button
            onClick={() => setReplicas((r) => Math.max(0, r - 1))}
            disabled={busy}
            aria-label="menos"
          >
            −
          </button>
          <input
            type="number"
            min={0}
            max={1000}
            value={replicas}
            onChange={(e) => setReplicas(Math.max(0, Number(e.target.value) || 0))}
            disabled={busy}
          />
          <button
            onClick={() => setReplicas((r) => Math.min(1000, r + 1))}
            disabled={busy}
            aria-label="más"
          >
            +
          </button>
        </div>

        <div className="insp-actions">
          <button
            className="btn primary"
            onClick={scale}
            disabled={busy || !canOperate || replicas === op.replicas}
          >
            Escalar
          </button>
          <button
            className="btn"
            onClick={restart}
            disabled={busy || !canOperate}
          >
            Reiniciar
          </button>
        </div>

        {fb && <div className={`insp-fb ${fb.tone}`}>{fb.text}</div>}
      </div>
    </aside>
  );
}
