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

  // --- complementos (solo clústeres) ---
  const cluster = sel.cluster;
  const [argocd, setArgocd] = useState(cluster?.argocd ?? false);
  const [installBusy, setInstallBusy] = useState(false);
  const [installFb, setInstallFb] = useState<Feedback | null>(null);
  const ipoll = useRef<number | null>(null);

  // --- proyectos GitOps (solo clústeres con ArgoCD) ---
  const [pName, setPName] = useState("");
  const [pRepo, setPRepo] = useState("");
  const [pPath, setPPath] = useState("");
  const [pNs, setPNs] = useState("default");
  const [addBusy, setAddBusy] = useState(false);
  const [addFb, setAddFb] = useState<Feedback | null>(null);

  // --- proyecto GitOps seleccionado (sync / rollback) ---
  const appOps = sel.app;
  const [gitBusy, setGitBusy] = useState(false);
  const [gitFb, setGitFb] = useState<Feedback | null>(null);

  async function gitopsAction(kind: "sync" | "rollback", verb: string) {
    if (!appOps) return;
    setGitBusy(true);
    setGitFb({ text: `${verb}…`, tone: "info" });
    try {
      const a = await postAction(appOps.clusterId, { kind, app: { name: appOps.name, repoURL: "", path: "", namespace: "argocd" } });
      let tries = 0;
      const iv = window.setInterval(async () => {
        tries++;
        try {
          const acts = await fetchActions(appOps.clusterId);
          const act = acts.find((x) => x.id === a.id);
          if (act?.status === "done") {
            window.clearInterval(iv);
            setGitBusy(false);
            setGitFb({ text: `${verb} ✓`, tone: "ok" });
          } else if (act?.status === "error") {
            window.clearInterval(iv);
            setGitBusy(false);
            setGitFb({ text: `error: ${act.error ?? "desconocido"}`, tone: "err" });
          } else if (tries > 30) {
            window.clearInterval(iv);
            setGitBusy(false);
            setGitFb({ text: "sin confirmación aún…", tone: "err" });
          }
        } catch {
          /* reintenta */
        }
      }, 2000);
    } catch (e) {
      setGitBusy(false);
      setGitFb({ text: `no se pudo encolar: ${String(e)}`, tone: "err" });
    }
  }

  useEffect(() => {
    setName(annotation.displayName ?? "");
    setColor(annotation.color ?? "");
    setNote(annotation.note ?? "");
    setReplicas(op?.replicas ?? 0);
    setArgocd(cluster?.argocd ?? false);
    setSavedFb(null);
    setOpFb(null);
    setInstallFb(null);
    setAddFb(null);
    setGitFb(null);
    setPName("");
    setPRepo("");
    setPPath("");
    setPNs("default");
    return stopPoll;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sel.key]);

  async function addProject() {
    if (!cluster) return;
    if (!pName.trim() || !pRepo.trim()) {
      setAddFb({ text: "nombre y repo son obligatorios", tone: "err" });
      return;
    }
    setAddBusy(true);
    setAddFb({ text: "registrando proyecto…", tone: "info" });
    try {
      const a = await postAction(cluster.clusterId, {
        kind: "addapp",
        app: {
          name: pName.trim(),
          repoURL: pRepo.trim(),
          path: pPath.trim(),
          namespace: pNs.trim() || "default",
        },
      });
      let tries = 0;
      const iv = window.setInterval(async () => {
        tries++;
        try {
          const acts = await fetchActions(cluster.clusterId);
          const st = acts.find((x) => x.id === a.id)?.status;
          if (st === "done") {
            window.clearInterval(iv);
            setAddBusy(false);
            setAddFb({ text: "proyecto registrado ✓ — ArgoCD lo sincronizará", tone: "ok" });
            setPName("");
            setPRepo("");
            setPPath("");
          } else if (st === "error") {
            window.clearInterval(iv);
            setAddBusy(false);
            const err = acts.find((x) => x.id === a.id)?.error;
            setAddFb({ text: `error: ${err ?? "desconocido"}`, tone: "err" });
          } else if (tries > 30) {
            window.clearInterval(iv);
            setAddBusy(false);
            setAddFb({ text: "sin confirmación aún…", tone: "err" });
          }
        } catch {
          /* reintenta */
        }
      }, 2000);
    } catch (e) {
      setAddBusy(false);
      setAddFb({ text: `no se pudo encolar: ${String(e)}`, tone: "err" });
    }
  }

  function stopPoll() {
    if (poll.current) window.clearInterval(poll.current);
    if (ipoll.current) window.clearInterval(ipoll.current);
    poll.current = null;
    ipoll.current = null;
  }

  async function installArgo() {
    if (!cluster) return;
    setInstallBusy(true);
    setInstallFb({ text: "instalando ArgoCD… (puede tardar ~1 min)", tone: "info" });
    try {
      const a = await postAction(cluster.clusterId, { kind: "install", addon: "argocd" });
      let tries = 0;
      ipoll.current = window.setInterval(async () => {
        tries++;
        try {
          const acts = await fetchActions(cluster.clusterId);
          const st = acts.find((x) => x.id === a.id)?.status;
          if (st === "done") {
            window.clearInterval(ipoll.current!);
            setInstallBusy(false);
            setArgocd(true);
            setInstallFb({ text: "ArgoCD instalado ✓", tone: "ok" });
          } else if (st === "error") {
            window.clearInterval(ipoll.current!);
            setInstallBusy(false);
            const err = acts.find((x) => x.id === a.id)?.error;
            setInstallFb({ text: `error: ${err ?? "desconocido"}`, tone: "err" });
          } else if (tries > 90) {
            window.clearInterval(ipoll.current!);
            setInstallBusy(false);
            setInstallFb({ text: "sin confirmación aún (¿agente offline?)", tone: "err" });
          }
        } catch {
          /* reintenta */
        }
      }, 2000);
    } catch (e) {
      setInstallBusy(false);
      setInstallFb({ text: `no se pudo encolar: ${String(e)}`, tone: "err" });
    }
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

        {/* ---- proyecto GitOps (sync / rollback) ---- */}
        {appOps && (
          <>
            <div className="insp-section">Proyecto GitOps</div>
            <div className="proj-status">
              <span
                className="proj-dot"
                style={{
                  background:
                    appOps.health === "Degraded" || appOps.health === "Missing"
                      ? "#ff7c7c"
                      : appOps.sync === "OutOfSync" || appOps.health === "Progressing"
                        ? "#F0932B"
                        : "var(--good)",
                }}
              />
              <span className="proj-status-text">
                {appOps.sync} · {appOps.health}
              </span>
            </div>
            <div className="insp-actions">
              <button
                className="btn primary"
                onClick={() => gitopsAction("sync", "sincronizar")}
                disabled={gitBusy || !appOps.online}
              >
                Sincronizar
              </button>
              <button
                className="btn"
                onClick={() => gitopsAction("rollback", "revertir")}
                disabled={gitBusy || !appOps.online}
              >
                Revertir
              </button>
            </div>
            {gitFb && <div className={`insp-fb ${gitFb.tone}`}>{gitFb.text}</div>}
          </>
        )}

        {/* ---- complementos (solo clústeres) ---- */}
        {cluster && (
          <>
            <div className="insp-section">Complementos</div>
            <div className="addon-row">
              <div className="addon-meta">
                <span className="addon-name">ArgoCD</span>
                <span className="addon-desc">GitOps · despliegue continuo</span>
              </div>
              {argocd ? (
                <span className="addon-installed">instalado ✓</span>
              ) : (
                <button
                  className="btn"
                  onClick={installArgo}
                  disabled={installBusy || !cluster.online}
                >
                  {installBusy ? "instalando…" : "Instalar"}
                </button>
              )}
            </div>
            {installFb && <div className={`insp-fb ${installFb.tone}`}>{installFb.text}</div>}
            {!argocd && (
              <div className="insp-hint-sm">
                El agente aplicará el manifiesto vetado de ArgoCD (v2.11.7). Requiere
                permisos ampliados — ver <span className="mono">deploy/agent-addons.yaml</span>.
              </div>
            )}
          </>
        )}

        {/* ---- proyectos GitOps (clúster con ArgoCD) ---- */}
        {cluster && argocd && (
          <>
            <div className="insp-section">Proyectos (GitOps)</div>
            {cluster.apps.length === 0 && (
              <div className="insp-hint-sm">Aún no hay proyectos. Añade uno abajo.</div>
            )}
            {cluster.apps.map((app) => (
              <div className="proj-row" key={app.name}>
                <span
                  className="proj-dot"
                  style={{
                    background:
                      app.health === "Degraded" || app.health === "Missing"
                        ? "#ff7c7c"
                        : app.sync === "OutOfSync" || app.health === "Progressing"
                          ? "#F0932B"
                          : "var(--good)",
                  }}
                />
                <div className="proj-meta">
                  <span className="proj-name">{app.name}</span>
                  <span className="proj-sub">
                    {app.sync} · {app.health}
                  </span>
                </div>
              </div>
            ))}

            <div className="proj-form">
              <input className="insp-input" placeholder="nombre del proyecto" value={pName}
                onChange={(e) => setPName(e.target.value)} disabled={addBusy} />
              <input className="insp-input" placeholder="https://github.com/org/repo" value={pRepo}
                onChange={(e) => setPRepo(e.target.value)} disabled={addBusy} />
              <input className="insp-input" placeholder="ruta (p. ej. manifests/)" value={pPath}
                onChange={(e) => setPPath(e.target.value)} disabled={addBusy} />
              <input className="insp-input" placeholder="namespace destino" value={pNs}
                onChange={(e) => setPNs(e.target.value)} disabled={addBusy} />
              <button className="btn primary" onClick={addProject} disabled={addBusy || !cluster.online}>
                {addBusy ? "registrando…" : "Añadir proyecto"}
              </button>
            </div>
            {addFb && <div className={`insp-fb ${addFb.tone}`}>{addFb.text}</div>}
          </>
        )}

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
