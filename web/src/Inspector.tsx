// Panel Inspector: al seleccionar una entidad del mapa permite EDITARLA (alias,
// color, nota — metadatos que no tocan el clúster) y, si es una carga, OPERARLA
// (escalar/reiniciar). Las ediciones se guardan en el control plane y las ven
// todos; las acciones viajan al agente y se ejecutan en el clúster.
import { useEffect, useRef, useState } from "react";
import {
  postAction,
  fetchActions,
  putAnnotation,
  type ActionStatus,
  type Annotation,
} from "./api";
import type { Selection } from "./ServiceNode";

interface Feedback {
  text: string;
  tone: "info" | "ok" | "err";
}

// Paleta propia (los colores del mapa). "" = color por defecto.
const SWATCHES = ["", "#5B57E0", "#12B5A5", "#2D74DA", "#E7476B", "#F0932B", "#0E9E6E", "#5A6577"];

export function Inspector({
  sel,
  annotation,
  onClose,
  onSaved,
}: {
  sel: Selection;
  annotation: Annotation;
  onClose: () => void;
  onSaved: () => void;
}) {
  const op = sel.op;

  // --- edición de metadatos ---
  const [name, setName] = useState(annotation.displayName ?? "");
  const [color, setColor] = useState(annotation.color ?? "");
  const [note, setNote] = useState(annotation.note ?? "");
  const [savedFb, setSavedFb] = useState<Feedback | null>(null);

  // --- operaciones (solo cargas) ---
  const [replicas, setReplicas] = useState(op?.replicas ?? 0);
  const [busy, setBusy] = useState(false);
  const [opFb, setOpFb] = useState<Feedback | null>(null);
  const poll = useRef<number | null>(null);

  useEffect(() => {
    setName(annotation.displayName ?? "");
    setColor(annotation.color ?? "");
    setNote(annotation.note ?? "");
    setReplicas(op?.replicas ?? 0);
    setSavedFb(null);
    setOpFb(null);
    return stopPoll;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sel.key]);

  function stopPoll() {
    if (poll.current) window.clearInterval(poll.current);
    poll.current = null;
  }

  const dirty =
    name !== (annotation.displayName ?? "") ||
    color !== (annotation.color ?? "") ||
    note !== (annotation.note ?? "");

  async function save() {
    setSavedFb({ text: "guardando…", tone: "info" });
    try {
      await putAnnotation(sel.key, { displayName: name.trim(), color, note: note.trim() });
      setSavedFb({ text: "guardado ✓", tone: "ok" });
      onSaved();
    } catch (e) {
      setSavedFb({ text: `no se pudo guardar: ${String(e)}`, tone: "err" });
    }
  }

  function track(id: string, verb: string) {
    stopPoll();
    let tries = 0;
    poll.current = window.setInterval(async () => {
      tries++;
      try {
        const actions = await fetchActions(op!.clusterId);
        const st = actions.find((x) => x.id === id)?.status as ActionStatus | undefined;
        if (st === "done") {
          stopPoll();
          setBusy(false);
          setOpFb({ text: `${verb} aplicado ✓`, tone: "ok" });
        } else if (st === "error") {
          stopPoll();
          setBusy(false);
          const err = actions.find((x) => x.id === id)?.error;
          setOpFb({ text: `error: ${err ?? "desconocido"}`, tone: "err" });
        } else if (tries > 20) {
          stopPoll();
          setBusy(false);
          setOpFb({ text: `${verb}: sin confirmación (¿agente offline?)`, tone: "err" });
        }
      } catch {
        /* reintenta */
      }
    }, 2000);
  }

  async function doOp(kind: "scale" | "restart") {
    if (!op) return;
    setBusy(true);
    setOpFb({ text: kind === "scale" ? "escalando…" : "reiniciando…", tone: "info" });
    try {
      const a = await postAction(op.clusterId, {
        kind,
        namespace: op.namespace,
        workload: op.workload,
        workloadKind: op.workloadKind,
        replicas: kind === "scale" ? replicas : op.replicas,
      });
      track(a.id, kind === "scale" ? `escalar a ${replicas}` : "reinicio");
    } catch (e) {
      setBusy(false);
      setOpFb({ text: `no se pudo encolar: ${String(e)}`, tone: "err" });
    }
  }

  const canOperate = op?.online ?? false;

  return (
    <aside className="inspector">
      <div className="insp-head">
        <div>
          <div className="insp-title">{annotation.displayName || sel.title}</div>
          <div className="insp-sub">
            {sel.kind} · {sel.subtitle}
            {annotation.displayName && <> · <span className="insp-real">{sel.title}</span></>}
          </div>
        </div>
        <button className="insp-x" onClick={onClose} aria-label="cerrar">
          ×
        </button>
      </div>

      <div className="insp-body">
        {/* ---- editar el mapa ---- */}
        <div className="insp-section">Editar</div>

        <label className="insp-label">Alias</label>
        <input
          className="insp-input"
          value={name}
          placeholder={sel.title}
          onChange={(e) => setName(e.target.value)}
        />

        <label className="insp-label">Color</label>
        <div className="insp-swatches">
          {SWATCHES.map((c) => (
            <button
              key={c || "default"}
              className={`swatch${color === c ? " on" : ""}${c === "" ? " reset" : ""}`}
              style={c ? { background: c } : undefined}
              title={c || "por defecto"}
              onClick={() => setColor(c)}
            />
          ))}
        </div>

        <label className="insp-label">Nota</label>
        <textarea
          className="insp-input insp-note-input"
          value={note}
          placeholder="dueño, propósito, contacto…"
          onChange={(e) => setNote(e.target.value)}
          rows={2}
        />

        <button className="btn primary" onClick={save} disabled={!dirty}>
          Guardar
        </button>
        {savedFb && <div className={`insp-fb ${savedFb.tone}`}>{savedFb.text}</div>}

        {/* ---- operar (solo cargas) ---- */}
        {op && (
          <>
            <div className="insp-section">Operar</div>
            {!canOperate && (
              <div className="insp-note err">
                El clúster está offline: las acciones no se ejecutarán hasta que vuelva a latir.
              </div>
            )}
            <label className="insp-label">Réplicas</label>
            <div className="insp-stepper">
              <button onClick={() => setReplicas((r) => Math.max(0, r - 1))} disabled={busy} aria-label="menos">
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
              <button onClick={() => setReplicas((r) => Math.min(1000, r + 1))} disabled={busy} aria-label="más">
                +
              </button>
            </div>
            <div className="insp-actions">
              <button
                className="btn primary"
                onClick={() => doOp("scale")}
                disabled={busy || !canOperate || replicas === op.replicas}
              >
                Escalar
              </button>
              <button className="btn" onClick={() => doOp("restart")} disabled={busy || !canOperate}>
                Reiniciar
              </button>
            </div>
            {opFb && <div className={`insp-fb ${opFb.tone}`}>{opFb.text}</div>}
          </>
        )}
      </div>
    </aside>
  );
}
