// Panel Inspector: edita metadatos, opera cargas, gestiona complementos del
// clúster (catálogo: seguridad, redes, monitoreo, GitOps) y proyectos GitOps
// (sincronizar/revertir + árbol de recursos). Todo desde la interfaz.
import { useEffect, useRef, useState } from "react";
import {
  postAction,
  fetchActions,
  putAnnotation,
  type Annotation,
  type AddonInfo,
} from "./api";
import type { Selection } from "./ServiceNode";

interface Feedback {
  text: string;
  tone: "info" | "ok" | "err";
}

const SWATCHES = ["", "#5B57E0", "#12B5A5", "#2D74DA", "#E7476B", "#F0932B", "#0E9E6E", "#5A6577"];

const CATEGORIES: { key: string; label: string }[] = [
  { key: "gitops", label: "GitOps" },
  { key: "seguridad", label: "Seguridad" },
  { key: "redes", label: "Redes" },
  { key: "monitoreo", label: "Monitoreo" },
];

function stateColor(sync: string, health: string): string {
  if (health === "Degraded" || health === "Missing") return "#ff7c7c";
  if (sync === "OutOfSync" || health === "Progressing") return "#F0932B";
  return "var(--good)";
}

export function Inspector({
  sel,
  annotation,
  addons,
  onClose,
  onSaved,
}: {
  sel: Selection;
  annotation: Annotation;
  addons: AddonInfo[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const op = sel.op;
  const cluster = sel.cluster;
  const appOps = sel.app;

  // edición de metadatos
  const [name, setName] = useState(annotation.displayName ?? "");
  const [color, setColor] = useState(annotation.color ?? "");
  const [note, setNote] = useState(annotation.note ?? "");
  const [savedFb, setSavedFb] = useState<Feedback | null>(null);

  // operaciones de carga
  const [replicas, setReplicas] = useState(op?.replicas ?? 0);
  const [busy, setBusy] = useState(false);
  const [opFb, setOpFb] = useState<Feedback | null>(null);
  const poll = useRef<number | null>(null);

  // complementos
  const [installingKey, setInstallingKey] = useState<string | null>(null);
  const [installFb, setInstallFb] = useState<Feedback | null>(null);
  const [justInstalled, setJustInstalled] = useState<string[]>([]);
  const [formKey, setFormKey] = useState<string | null>(null); // addon con formulario abierto
  const [formVals, setFormVals] = useState<Record<string, string>>({});

  // proyectos GitOps (form + sync/rollback)
  const [pName, setPName] = useState("");
  const [pRepo, setPRepo] = useState("");
  const [pPath, setPPath] = useState("");
  const [pNs, setPNs] = useState("default");
  const [addBusy, setAddBusy] = useState(false);
  const [addFb, setAddFb] = useState<Feedback | null>(null);
  const [gitBusy, setGitBusy] = useState(false);
  const [gitFb, setGitFb] = useState<Feedback | null>(null);

  useEffect(() => {
    setName(annotation.displayName ?? "");
    setColor(annotation.color ?? "");
    setNote(annotation.note ?? "");
    setReplicas(op?.replicas ?? 0);
    setSavedFb(null);
    setOpFb(null);
    setInstallFb(null);
    setAddFb(null);
    setGitFb(null);
    setFormKey(null);
    setPName("");
    setPRepo("");
    setPPath("");
    setPNs("default");
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

  // Sigue una acción hasta done/error y llama a cb con el resultado.
  function trackAction(clusterId: string, id: string, cb: (ok: boolean, err?: string) => void) {
    let tries = 0;
    const iv = window.setInterval(async () => {
      tries++;
      try {
        const acts = await fetchActions(clusterId);
        const act = acts.find((x) => x.id === id);
        if (act?.status === "done") {
          window.clearInterval(iv);
          cb(true);
        } else if (act?.status === "error") {
          window.clearInterval(iv);
          cb(false, act.error);
        } else if (tries > 90) {
          window.clearInterval(iv);
          cb(false, "sin confirmación (¿agente offline?)");
        }
      } catch {
        /* reintenta */
      }
    }, 2000);
  }

  // ---- operar carga ----
  async function doOp(kind: "scale" | "restart") {
    if (!op) return;
    setBusy(true);
    setOpFb({ text: kind === "scale" ? "escalando…" : "reiniciando…", tone: "info" });
    try {
      const a = await postAction(op.clusterId, {
        kind, namespace: op.namespace, workload: op.workload,
        workloadKind: op.workloadKind, replicas: kind === "scale" ? replicas : op.replicas,
      });
      const verb = kind === "scale" ? `escalar a ${replicas}` : "reinicio";
      trackAction(op.clusterId, a.id, (ok, err) => {
        setBusy(false);
        setOpFb(ok ? { text: `${verb} aplicado ✓`, tone: "ok" } : { text: `error: ${err}`, tone: "err" });
      });
    } catch (e) {
      setBusy(false);
      setOpFb({ text: `no se pudo encolar: ${String(e)}`, tone: "err" });
    }
  }

  // ---- instalar un complemento del catálogo ----
  // Si tiene parámetros editables, abre el formulario; si no, instala directo.
  function startInstall(addon: AddonInfo) {
    const params = addon.params ?? [];
    if (params.length > 0) {
      const defs: Record<string, string> = {};
      for (const p of params) defs[p.key] = p.default ?? "";
      setFormVals(defs);
      setFormKey(addon.key);
      return;
    }
    install(addon, {});
  }

  async function install(addon: AddonInfo, values: Record<string, string>) {
    if (!cluster) return;
    setFormKey(null);
    setInstallingKey(addon.key);
    setInstallFb({ text: `instalando ${addon.name}… (puede tardar)`, tone: "info" });
    try {
      const a = await postAction(cluster.clusterId, {
        kind: "install",
        addon: addon.key,
        values: Object.keys(values).length ? values : undefined,
      });
      trackAction(cluster.clusterId, a.id, (ok, err) => {
        setInstallingKey(null);
        if (ok) {
          setJustInstalled((s) => [...s, addon.key]);
          setInstallFb({ text: `${addon.name} instalado ✓`, tone: "ok" });
        } else {
          setInstallFb({ text: `error instalando ${addon.name}: ${err}`, tone: "err" });
        }
      });
    } catch (e) {
      setInstallingKey(null);
      setInstallFb({ text: `no se pudo encolar: ${String(e)}`, tone: "err" });
    }
  }

  const isInstalled = (key: string) =>
    (cluster?.installedAddons.includes(key) ?? false) || justInstalled.includes(key);

  // ---- proyectos GitOps ----
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
        app: { name: pName.trim(), repoURL: pRepo.trim(), path: pPath.trim(), namespace: pNs.trim() || "default" },
      });
      trackAction(cluster.clusterId, a.id, (ok, err) => {
        setAddBusy(false);
        if (ok) {
          setAddFb({ text: "proyecto registrado ✓ — ArgoCD lo sincronizará", tone: "ok" });
          setPName(""); setPRepo(""); setPPath("");
        } else {
          setAddFb({ text: `error: ${err}`, tone: "err" });
        }
      });
    } catch (e) {
      setAddBusy(false);
      setAddFb({ text: `no se pudo encolar: ${String(e)}`, tone: "err" });
    }
  }

  async function gitopsAction(kind: "sync" | "rollback", verb: string) {
    if (!appOps) return;
    setGitBusy(true);
    setGitFb({ text: `${verb}…`, tone: "info" });
    try {
      const a = await postAction(appOps.clusterId, {
        kind, app: { name: appOps.name, repoURL: "", path: "", namespace: "argocd" },
      });
      trackAction(appOps.clusterId, a.id, (ok, err) => {
        setGitBusy(false);
        setGitFb(ok ? { text: `${verb} ✓`, tone: "ok" } : { text: `error: ${err}`, tone: "err" });
      });
    } catch (e) {
      setGitBusy(false);
      setGitFb({ text: `no se pudo encolar: ${String(e)}`, tone: "err" });
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
          </div>
        </div>
        <button className="insp-x" onClick={onClose} aria-label="cerrar">×</button>
      </div>

      <div className="insp-body">
        {/* ---- editar ---- */}
        <div className="insp-section">Editar</div>
        <label className="insp-label">Alias</label>
        <input className="insp-input" value={name} placeholder={sel.title} onChange={(e) => setName(e.target.value)} />
        <label className="insp-label">Color</label>
        <div className="insp-swatches">
          {SWATCHES.map((c) => (
            <button key={c || "default"} className={`swatch${color === c ? " on" : ""}${c === "" ? " reset" : ""}`}
              style={c ? { background: c } : undefined} title={c || "por defecto"} onClick={() => setColor(c)} />
          ))}
        </div>
        <label className="insp-label">Nota</label>
        <textarea className="insp-input insp-note-input" value={note} placeholder="dueño, propósito, contacto…"
          onChange={(e) => setNote(e.target.value)} rows={2} />
        <button className="btn primary" onClick={save} disabled={!dirty}>Guardar</button>
        {savedFb && <div className={`insp-fb ${savedFb.tone}`}>{savedFb.text}</div>}

        {/* ---- proyecto GitOps: estado + sync/rollback + árbol ---- */}
        {appOps && (
          <>
            <div className="insp-section">Proyecto GitOps</div>
            <div className="proj-status">
              <span className="proj-dot" style={{ background: stateColor(appOps.sync, appOps.health) }} />
              <span className="proj-status-text">{appOps.sync} · {appOps.health}</span>
            </div>
            <div className="insp-actions">
              <button className="btn primary" onClick={() => gitopsAction("sync", "sincronizar")} disabled={gitBusy || !appOps.online}>
                Sincronizar
              </button>
              <button className="btn" onClick={() => gitopsAction("rollback", "revertir")} disabled={gitBusy || !appOps.online}>
                Revertir
              </button>
            </div>
            {gitFb && <div className={`insp-fb ${gitFb.tone}`}>{gitFb.text}</div>}

            <label className="insp-label">Recursos ({appOps.resources.length})</label>
            <div className="restree">
              {appOps.resources.length === 0 && <div className="insp-hint-sm">sin recursos aún</div>}
              {appOps.resources.map((r) => (
                <div className="res-row" key={`${r.kind}/${r.namespace}/${r.name}`}>
                  <span className="res-dot" style={{ background: stateColor(r.status ?? "", r.health ?? "") }} />
                  <span className="res-kind">{r.kind}</span>
                  <span className="res-name">{r.name}</span>
                </div>
              ))}
            </div>
          </>
        )}

        {/* ---- complementos (catálogo) del clúster ---- */}
        {cluster && (
          <>
            <div className="insp-section">Complementos</div>
            {CATEGORIES.map((cat) => {
              const items = addons.filter((a) => a.category === cat.key);
              if (items.length === 0) return null;
              return (
                <div className="addon-cat" key={cat.key}>
                  <div className="addon-cat-label">{cat.label}</div>
                  {items.map((a) => (
                    <div key={a.key}>
                      <div className="addon-row">
                        <div className="addon-meta">
                          <span className="addon-name">{a.name}</span>
                          <span className="addon-desc">{a.description}</span>
                        </div>
                        {isInstalled(a.key) ? (
                          <span className="addon-installed">instalado ✓</span>
                        ) : (
                          <button className="btn" onClick={() => startInstall(a)}
                            disabled={installingKey !== null || !cluster.online}>
                            {installingKey === a.key ? "instalando…" : "Instalar"}
                          </button>
                        )}
                      </div>
                      {formKey === a.key && (
                        <div className="addon-form">
                          {(a.params ?? []).map((p) => (
                            <div className="addon-field" key={p.key}>
                              <label className="insp-label">{p.label}</label>
                              <input
                                className="insp-input"
                                type={p.type === "password" ? "password" : p.type === "int" ? "number" : "text"}
                                value={formVals[p.key] ?? ""}
                                placeholder={p.default || "(por defecto)"}
                                onChange={(e) => setFormVals((v) => ({ ...v, [p.key]: e.target.value }))}
                              />
                            </div>
                          ))}
                          <div className="insp-actions">
                            <button className="btn primary" onClick={() => install(a, formVals)}>
                              Instalar {a.name}
                            </button>
                            <button className="btn" onClick={() => setFormKey(null)}>Cancelar</button>
                          </div>
                        </div>
                      )}
                    </div>
                  ))}
                </div>
              );
            })}
            {installFb && <div className={`insp-fb ${installFb.tone}`}>{installFb.text}</div>}
            <div className="insp-hint-sm">
              Catálogo cerrado y con versión fijada — el agente nunca aplica YAML arbitrario.
              Instalar requiere el RBAC opt-in (<span className="mono">deploy/agent-addons.yaml</span>).
            </div>

            {/* ---- proyectos GitOps (si ArgoCD está) ---- */}
            {isInstalled("argocd") && (
              <>
                <div className="insp-section">Proyectos (GitOps)</div>
                {cluster.apps.length === 0 && <div className="insp-hint-sm">Aún no hay proyectos. Añade uno abajo.</div>}
                {cluster.apps.map((app) => (
                  <div className="proj-row" key={app.name}>
                    <span className="proj-dot" style={{ background: stateColor(app.sync, app.health) }} />
                    <div className="proj-meta">
                      <span className="proj-name">{app.name}</span>
                      <span className="proj-sub">{app.sync} · {app.health}</span>
                    </div>
                  </div>
                ))}
                <div className="proj-form">
                  <input className="insp-input" placeholder="nombre del proyecto" value={pName} onChange={(e) => setPName(e.target.value)} disabled={addBusy} />
                  <input className="insp-input" placeholder="https://github.com/org/repo" value={pRepo} onChange={(e) => setPRepo(e.target.value)} disabled={addBusy} />
                  <input className="insp-input" placeholder="ruta (p. ej. manifests/)" value={pPath} onChange={(e) => setPPath(e.target.value)} disabled={addBusy} />
                  <input className="insp-input" placeholder="namespace destino" value={pNs} onChange={(e) => setPNs(e.target.value)} disabled={addBusy} />
                  <button className="btn primary" onClick={addProject} disabled={addBusy || !cluster.online}>
                    {addBusy ? "registrando…" : "Añadir proyecto"}
                  </button>
                </div>
                {addFb && <div className={`insp-fb ${addFb.tone}`}>{addFb.text}</div>}
              </>
            )}
          </>
        )}

        {/* ---- operar carga ---- */}
        {op && (
          <>
            <div className="insp-section">Operar</div>
            {!canOperate && <div className="insp-note err">El clúster está offline: las acciones no se ejecutarán hasta que vuelva a latir.</div>}
            <label className="insp-label">Réplicas</label>
            <div className="insp-stepper">
              <button onClick={() => setReplicas((r) => Math.max(0, r - 1))} disabled={busy} aria-label="menos">−</button>
              <input type="number" min={0} max={1000} value={replicas}
                onChange={(e) => setReplicas(Math.max(0, Number(e.target.value) || 0))} disabled={busy} />
              <button onClick={() => setReplicas((r) => Math.min(1000, r + 1))} disabled={busy} aria-label="más">+</button>
            </div>
            <div className="insp-actions">
              <button className="btn primary" onClick={() => doOp("scale")} disabled={busy || !canOperate || replicas === op.replicas}>Escalar</button>
              <button className="btn" onClick={() => doOp("restart")} disabled={busy || !canOperate}>Reiniciar</button>
            </div>
            {opFb && <div className={`insp-fb ${opFb.tone}`}>{opFb.text}</div>}
          </>
        )}
      </div>
    </aside>
  );
}
