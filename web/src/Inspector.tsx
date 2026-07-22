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
  const nodeOps = sel.node;

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

  // diagnóstico (logs / eventos)
  const [diag, setDiag] = useState<string | null>(null);
  const [diagBusy, setDiagBusy] = useState(false);
  const [diagFb, setDiagFb] = useState<Feedback | null>(null);

  // gestión del nodo (cordon / drain)
  const [nodeBusy, setNodeBusy] = useState(false);
  const [nodeFb, setNodeFb] = useState<Feedback | null>(null);
  const [drainConfirm, setDrainConfirm] = useState(false);
  const [drainOut, setDrainOut] = useState<string | null>(null);

  // crear namespace (con cuotas)
  const [nsName, setNsName] = useState("");
  const [nsCPU, setNsCPU] = useState("");
  const [nsMem, setNsMem] = useState("");
  const [nsBusy, setNsBusy] = useState(false);
  const [nsFb, setNsFb] = useState<Feedback | null>(null);

  // emisor TLS (cert-manager)
  const [issEmail, setIssEmail] = useState("");
  const [issEnv, setIssEnv] = useState<"staging" | "production">("staging");
  const [issBusy, setIssBusy] = useState(false);
  const [issFb, setIssFb] = useState<Feedback | null>(null);

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

  // ---- diagnóstico: pedir logs o eventos al agente y mostrar la salida ----
  async function runDiag(kind: "logs" | "events") {
    const clusterId = op?.clusterId ?? cluster?.clusterId;
    const ns = op?.namespace ?? "default";
    if (!clusterId) return;
    setDiagBusy(true);
    setDiag(null);
    setDiagFb({ text: kind === "logs" ? "pidiendo logs…" : "pidiendo eventos…", tone: "info" });
    try {
      const a = await postAction(clusterId, {
        kind,
        namespace: ns,
        workload: kind === "logs" ? op?.workload : undefined,
        workloadKind: op?.workloadKind,
      });
      const poll = window.setInterval(async () => {
        try {
          const acts = await fetchActions(clusterId);
          const act = acts.find((x) => x.id === a.id);
          if (act?.status === "done") {
            window.clearInterval(poll);
            setDiagBusy(false);
            setDiagFb(null);
            setDiag(act.output || "(sin salida)");
          } else if (act?.status === "error") {
            window.clearInterval(poll);
            setDiagBusy(false);
            setDiagFb({ text: `error: ${act.error}`, tone: "err" });
          }
        } catch {
          /* reintenta */
        }
      }, 1500);
    } catch (e) {
      setDiagBusy(false);
      setDiagFb({ text: `no se pudo pedir: ${String(e)}`, tone: "err" });
    }
  }

  // ---- gestión del nodo (cordon / uncordon / drain) ----
  async function nodeAction(kind: "cordon" | "uncordon" | "drain", verb: string) {
    if (!nodeOps) return;
    setNodeBusy(true);
    setDrainOut(null);
    setNodeFb({ text: `${verb}…`, tone: "info" });
    try {
      const a = await postAction(nodeOps.clusterId, { kind, node: nodeOps.name });
      const iv = window.setInterval(async () => {
        try {
          const acts = await fetchActions(nodeOps.clusterId);
          const act = acts.find((x) => x.id === a.id);
          if (act?.status === "done") {
            window.clearInterval(iv);
            setNodeBusy(false);
            setNodeFb({ text: `${verb} ✓`, tone: "ok" });
            if (act.output) setDrainOut(act.output);
          } else if (act?.status === "error") {
            window.clearInterval(iv);
            setNodeBusy(false);
            setNodeFb({ text: `error: ${act.error}`, tone: "err" });
          }
        } catch {
          /* reintenta */
        }
      }, 1500);
    } catch (e) {
      setNodeBusy(false);
      setNodeFb({ text: `no se pudo encolar: ${String(e)}`, tone: "err" });
    }
  }

  // ---- crear namespace con cuotas ----
  async function createNS() {
    if (!cluster || !nsName.trim()) {
      setNsFb({ text: "pon un nombre de namespace", tone: "err" });
      return;
    }
    setNsBusy(true);
    setNsFb({ text: "creando namespace…", tone: "info" });
    try {
      const a = await postAction(cluster.clusterId, {
        kind: "createns",
        ns: { name: nsName.trim(), cpu: nsCPU.trim() || undefined, memory: nsMem.trim() || undefined },
      });
      trackAction(cluster.clusterId, a.id, (ok, err) => {
        setNsBusy(false);
        if (ok) {
          setNsFb({ text: `namespace ${nsName.trim()} creado ✓`, tone: "ok" });
          setNsName(""); setNsCPU(""); setNsMem("");
        } else {
          setNsFb({ text: `error: ${err}`, tone: "err" });
        }
      });
    } catch (e) {
      setNsBusy(false);
      setNsFb({ text: `no se pudo encolar: ${String(e)}`, tone: "err" });
    }
  }

  // ---- emisor TLS (ClusterIssuer de cert-manager) ----
  async function createIssuer() {
    if (!cluster) return;
    if (!issEmail.includes("@")) {
      setIssFb({ text: "pon un email válido (cuenta ACME)", tone: "err" });
      return;
    }
    setIssBusy(true);
    setIssFb({ text: "creando emisor TLS…", tone: "info" });
    try {
      const a = await postAction(cluster.clusterId, {
        kind: "issuer",
        issuer: { email: issEmail.trim(), environment: issEnv },
      });
      trackAction(cluster.clusterId, a.id, (ok, err) => {
        setIssBusy(false);
        if (ok) {
          setIssFb({
            text: `emisor letsencrypt-${issEnv} creado ✓ — anota tus Ingress con cert-manager.io/cluster-issuer: letsencrypt-${issEnv}`,
            tone: "ok",
          });
        } else {
          setIssFb({ text: `error: ${err}`, tone: "err" });
        }
      });
    } catch (e) {
      setIssBusy(false);
      setIssFb({ text: `no se pudo encolar: ${String(e)}`, tone: "err" });
    }
  }

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
                          <span className="addon-state">
                            <span className="addon-installed">instalado ✓</span>
                            {(a.params?.length ?? 0) > 0 && (
                              <button className="addon-edit" onClick={() => startInstall(a)}
                                disabled={installingKey !== null || !cluster.online}>
                                editar
                              </button>
                            )}
                          </span>
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
                              {isInstalled(a.key) ? "Actualizar" : "Instalar"} {a.name}
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

            {/* acceso a Grafana cuando el monitoreo está instalado */}
            {isInstalled("kube-prometheus-stack") && (
              <>
                <div className="insp-section">Acceder a Grafana</div>
                <div className="grafana-access">
                  <code className="grafana-cmd">
                    kubectl -n monitoring port-forward svc/kube-prometheus-stack-grafana 3000:80
                  </code>
                  <div className="grafana-links">
                    <button
                      className="btn"
                      onClick={() =>
                        navigator.clipboard?.writeText(
                          "kubectl -n monitoring port-forward svc/kube-prometheus-stack-grafana 3000:80",
                        )
                      }
                    >
                      Copiar comando
                    </button>
                    <a className="btn primary grafana-open" href="http://localhost:3000" target="_blank" rel="noreferrer">
                      Abrir Grafana ↗
                    </a>
                  </div>
                  <div className="insp-hint-sm">
                    Usuario <span className="mono">admin</span> · la contraseña que pusiste al instalar
                    (o <span className="mono">prom-operator</span> por defecto). Corre el port-forward y abre el enlace.
                  </div>
                </div>
              </>
            )}

            {/* ---- crear namespace con cuotas ---- */}
            <div className="insp-section">Crear namespace</div>
            <div className="insp-hint-sm">
              Ordena el clúster por equipos/proyectos. Las cuotas (opcionales)
              limitan el TOTAL de CPU/memoria del namespace (ResourceQuota).
            </div>
            <div className="proj-form">
              <input className="insp-input" placeholder="nombre (p. ej. equipo-web)"
                value={nsName} onChange={(e) => setNsName(e.target.value)} disabled={nsBusy} />
              <div className="ns-quota-row">
                <input className="insp-input" placeholder="CPU (p. ej. 2)"
                  value={nsCPU} onChange={(e) => setNsCPU(e.target.value)} disabled={nsBusy} />
                <input className="insp-input" placeholder="memoria (p. ej. 4Gi)"
                  value={nsMem} onChange={(e) => setNsMem(e.target.value)} disabled={nsBusy} />
              </div>
              <button className="btn primary" onClick={createNS} disabled={nsBusy || !cluster.online || !nsName.trim()}>
                {nsBusy ? "creando…" : "Crear namespace"}
              </button>
            </div>
            {nsFb && <div className={`insp-fb ${nsFb.tone}`}>{nsFb.text}</div>}

            {/* ---- emisor TLS (si cert-manager está) ---- */}
            {isInstalled("cert-manager") && (
              <>
                <div className="insp-section">Publicar con TLS (cert-manager)</div>
                <div className="insp-hint-sm">
                  Crea un emisor ACME (Let's Encrypt). Después, publicar un servicio con
                  HTTPS es anotar su Ingress con{" "}
                  <span className="mono">cert-manager.io/cluster-issuer</span>.
                </div>
                <div className="proj-form">
                  <label className="insp-label">Email (cuenta ACME · avisos de expiración)</label>
                  <input className="insp-input" type="email" placeholder="tú@dominio.com"
                    value={issEmail} onChange={(e) => setIssEmail(e.target.value)} disabled={issBusy} />
                  <label className="insp-label">Entorno</label>
                  <select className="insp-input" value={issEnv}
                    onChange={(e) => setIssEnv(e.target.value as "staging" | "production")} disabled={issBusy}>
                    <option value="staging">staging (pruebas, sin límites duros)</option>
                    <option value="production">production (certificados de verdad)</option>
                  </select>
                  <button className="btn primary" onClick={createIssuer} disabled={issBusy || !cluster.online}>
                    {issBusy ? "creando…" : `Crear emisor letsencrypt-${issEnv}`}
                  </button>
                </div>
                {issFb && <div className={`insp-fb ${issFb.tone}`}>{issFb.text}</div>}
              </>
            )}

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

        {/* ---- consumo vivo (metrics-server) ---- */}
        {sel.usage && (
          <div className="insp-usage">
            CPU <b>{sel.usage.cpum}m</b> · Memoria <b>{sel.usage.memMi}Mi</b>
            <span className="insp-usage-src"> (en uso ahora)</span>
          </div>
        )}

        {/* ---- pods e IPs de la carga ---- */}
        {(sel.pods?.length ?? 0) > 0 && (
          <>
            <div className="insp-section">Pods e IPs ({sel.pods!.length})</div>
            <div className="pods-table">
              {sel.pods!.map((p) => (
                <div className="pods-row" key={p.name}>
                  <span
                    className="pods-dot"
                    style={{ background: p.phase === "Running" ? "var(--good)" : "#F0932B" }}
                    title={p.phase || "?"}
                  />
                  <span className="pods-name" title={p.name}>{p.name}</span>
                  <span className="pods-ip mono">{p.ip || "—"}</span>
                  <span className="pods-node" title="nodo">{p.node || ""}</span>
                </div>
              ))}
            </div>
          </>
        )}

        {/* ---- gestión del nodo: cordon / drain / uncordon ---- */}
        {nodeOps && (
          <>
            <div className="insp-section">Gestionar nodo</div>
            {nodeOps.unschedulable && (
              <div className="insp-note err">Este nodo está ACORDONADO: no acepta pods nuevos.</div>
            )}
            <div className="insp-actions">
              {nodeOps.unschedulable ? (
                <button className="btn primary" onClick={() => nodeAction("uncordon", "reabriendo")}
                  disabled={nodeBusy || !nodeOps.online}>
                  Reabrir (uncordon)
                </button>
              ) : (
                <button className="btn" onClick={() => nodeAction("cordon", "acordonando")}
                  disabled={nodeBusy || !nodeOps.online}>
                  Acordonar
                </button>
              )}
              {drainConfirm ? (
                <>
                  <button className="btn danger" disabled={nodeBusy}
                    onClick={() => { setDrainConfirm(false); nodeAction("drain", "vaciando el nodo"); }}>
                    Sí, vaciar
                  </button>
                  <button className="btn" onClick={() => setDrainConfirm(false)}>No</button>
                </>
              ) : (
                <button className="btn danger-ghost" onClick={() => setDrainConfirm(true)}
                  disabled={nodeBusy || !nodeOps.online}>
                  Vaciar (drain)
                </button>
              )}
            </div>
            <div className="insp-hint-sm">
              Vaciar = acordonar + desalojar sus pods (respeta PodDisruptionBudgets;
              los DaemonSets se quedan). Para mantenimiento del servidor.
            </div>
            {nodeFb && <div className={`insp-fb ${nodeFb.tone}`}>{nodeFb.text}</div>}
            {drainOut && (
              <div className="diag-out-wrap">
                <pre className="diag-out">{drainOut}</pre>
                <button className="insp-x diag-close" onClick={() => setDrainOut(null)} aria-label="cerrar">×</button>
              </div>
            )}
          </>
        )}

        {/* ---- diagnóstico: logs y eventos sin salir de Atlas ---- */}
        {op && (
          <>
            <div className="insp-section">Diagnóstico</div>
            <div className="insp-actions">
              <button className="btn" onClick={() => runDiag("logs")} disabled={diagBusy || !op.online}>
                {diagBusy ? "pidiendo…" : "Ver logs"}
              </button>
              <button className="btn" onClick={() => runDiag("events")} disabled={diagBusy || !op.online}>
                Eventos del ns
              </button>
            </div>
            {diagFb && <div className={`insp-fb ${diagFb.tone}`}>{diagFb.text}</div>}
            {diag !== null && (
              <div className="diag-out-wrap">
                <pre className="diag-out">{diag}</pre>
                <button className="insp-x diag-close" onClick={() => setDiag(null)} aria-label="cerrar salida">×</button>
              </div>
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
