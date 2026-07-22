// Módulo de Servicios: TODO el ciclo de vida de los servicios del clúster en un
// solo apartado — instalar desde el catálogo, configurar los ya instalados
// (valores de Helm → upgrade), publicarlos (Ingress) y abrirlos. También adopta
// las rutas publicadas por fuera de Atlas.
import { useEffect, useMemo, useState, type FormEvent } from "react";
import {
  fetchActions,
  fetchAddons,
  fetchTopology,
  postAction,
  type AddonInfo,
  type ClusterView,
  type IngressInfo,
  type Topology,
  type Workload,
} from "./api";

const POLL_MS = 5000;

const CATEGORIES: { key: string; label: string }[] = [
  { key: "gitops", label: "GitOps" },
  { key: "seguridad", label: "Seguridad" },
  { key: "redes", label: "Redes" },
  { key: "monitoreo", label: "Monitoreo" },
];

interface Feedback {
  text: string;
  tone: "info" | "ok" | "err";
}

function urlOf(i: IngressInfo): string {
  return `${i.tls ? "https" : "http"}://${i.host}`;
}

/** ¿Está instalado este complemento en el clúster? (mismo criterio que el mapa) */
function detect(c: ClusterView, a: AddonInfo): Workload | undefined {
  return (c.snapshot?.workloads ?? []).find(
    (w) => w.namespace === a.namespace && w.name.includes(a.detectWorkload),
  );
}

/** Sigue una acción hasta done/error. */
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
      } else if (tries > 150) {
        window.clearInterval(iv);
        cb(false, "sin confirmación (¿agente offline?)");
      }
    } catch {
      /* reintenta */
    }
  }, 2000);
}

export function ServicesPanel({ onClose }: { onClose: () => void }) {
  const [topo, setTopo] = useState<Topology | null>(null);
  const [addons, setAddons] = useState<AddonInfo[]>([]);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    const load = async () => {
      try {
        const t = await fetchTopology();
        if (alive) {
          setTopo(t);
          setErr(null);
        }
      } catch (e) {
        if (alive) setErr(String(e));
      }
    };
    load();
    fetchAddons().then(setAddons).catch(() => {});
    const id = setInterval(load, POLL_MS);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, []);

  const clusters = topo?.clusters ?? [];

  return (
    <aside className="audit services">
      <div className="audit-head">
        <div className="audit-title">Servicios</div>
        <button className="insp-x" onClick={onClose} aria-label="cerrar">
          ×
        </button>
      </div>
      <div className="audit-body">
        {err && <div className="audit-empty err">sin conexión al control plane</div>}
        {!err && clusters.length === 0 && (
          <div className="audit-empty">esperando el primer clúster…</div>
        )}
        {clusters.map((c) => (
          <ClusterServices key={c.clusterId} cluster={c} addons={addons} />
        ))}
      </div>
    </aside>
  );
}

/** Todos los servicios de UN clúster: instalados + rutas + catálogo. */
function ClusterServices({ cluster, addons }: { cluster: ClusterView; addons: AddonInfo[] }) {
  // instalación / configuración (helm upgrade) en curso
  const [busyKey, setBusyKey] = useState<string | null>(null);
  const [fb, setFb] = useState<Feedback | null>(null);
  const [justInstalled, setJustInstalled] = useState<string[]>([]);
  // formulario abierto: "cfg:<addon>" (valores) | "pub:<addon|ns/svc>" (publicar)
  const [openForm, setOpenForm] = useState<string | null>(null);
  const [formVals, setFormVals] = useState<Record<string, string>>({});
  const [showCatalog, setShowCatalog] = useState(false);

  const installed = useMemo(
    () =>
      addons
        .map((a) => ({ addon: a, workload: detect(cluster, a) }))
        .filter((x) => x.workload || justInstalled.includes(x.addon.key)),
    [addons, cluster, justInstalled],
  );
  const notInstalled = useMemo(
    () => addons.filter((a) => !installed.some((i) => i.addon.key === a.key)),
    [addons, installed],
  );
  const ingresses = cluster.snapshot?.ingresses ?? [];
  const ingressFor = (a: AddonInfo): IngressInfo | null =>
    a.access
      ? (ingresses.find((i) => i.namespace === a.namespace && i.service === a.access!.service) ?? null)
      : null;
  // rutas publicadas que no son de complementos del catálogo (ni la propia GUI)
  const otherRoutes = ingresses.filter(
    (i) =>
      i.namespace !== "atlas-system" &&
      !addons.some((a) => a.access && a.namespace === i.namespace && a.access.service === i.service),
  );

  function openConfig(a: AddonInfo) {
    const defs: Record<string, string> = {};
    for (const p of a.params ?? []) defs[p.key] = p.default ?? "";
    setFormVals(defs);
    setOpenForm(openForm === `cfg:${a.key}` ? null : `cfg:${a.key}`);
  }

  async function install(a: AddonInfo, values: Record<string, string>, verb: string) {
    setOpenForm(null);
    setBusyKey(a.key);
    setFb({ text: `${verb} ${a.name}… (puede tardar unos minutos)`, tone: "info" });
    try {
      const act = await postAction(cluster.clusterId, {
        kind: "install",
        addon: a.key,
        values: Object.keys(values).length ? values : undefined,
      });
      trackAction(cluster.clusterId, act.id, (ok, err) => {
        setBusyKey(null);
        if (ok) {
          setJustInstalled((s) => (s.includes(a.key) ? s : [...s, a.key]));
          setFb({ text: `${a.name}: ${verb} completado ✓`, tone: "ok" });
        } else {
          setFb({ text: `${a.name}: error — ${err}`, tone: "err" });
        }
      });
    } catch (e) {
      setBusyKey(null);
      setFb({ text: `no se pudo encolar: ${e instanceof Error ? e.message : String(e)}`, tone: "err" });
    }
  }

  return (
    <div className="svc-cluster-block">
      <div className="svc-cluster-head">
        <span
          className="audit-dot"
          style={{ background: cluster.online ? "var(--good)" : "#ff7c7c" }}
        />
        <span className="svc-cluster-name">{cluster.name}</span>
        <span className="svc-cluster-sub">
          {installed.length} instalado(s) · {ingresses.length} ruta(s)
        </span>
      </div>

      {/* ---- instalados: estado + configurar + publicar/abrir ---- */}
      {installed.length === 0 && (
        <div className="audit-empty">
          nada instalado aún — abre el catálogo y despliega tu primer servicio
        </div>
      )}
      {installed.map(({ addon: a, workload: w }) => {
        const ing = ingressFor(a);
        const cfgOpen = openForm === `cfg:${a.key}`;
        const pubOpen = openForm === `pub:${a.key}`;
        return (
          <div className="svc-row" key={a.key}>
            <div className="svc-line">
              <span
                className="audit-dot"
                style={{ background: w ? (w.replicas > 0 ? "var(--good)" : "#F0932B") : "var(--faint)" }}
              />
              <div className="svc-main">
                <div className="svc-name">
                  {a.name}
                  <span className="svc-chip">{a.category}</span>
                </div>
                <div className="svc-meta">
                  {a.namespace}
                  {w ? ` · ${w.replicas} réplica(s)` : " · arrancando…"}
                  {ing && (
                    <>
                      {" · "}
                      <a className="svc-url" href={urlOf(ing)} target="_blank" rel="noreferrer">
                        {ing.host}
                      </a>
                    </>
                  )}
                </div>
              </div>
              <span className="svc-btns">
                {(a.params?.length ?? 0) > 0 && (
                  <button
                    className={`bar-btn${cfgOpen ? " active" : ""}`}
                    onClick={() => openConfig(a)}
                    disabled={busyKey !== null || !cluster.online}
                  >
                    {busyKey === a.key ? "aplicando…" : "Configurar"}
                  </button>
                )}
                {a.access &&
                  (ing ? (
                    <a className="bar-btn svc-open" href={urlOf(ing)} target="_blank" rel="noreferrer">
                      Abrir ↗
                    </a>
                  ) : (
                    <button
                      className={`bar-btn${pubOpen ? " active" : ""}`}
                      onClick={() => setOpenForm(pubOpen ? null : `pub:${a.key}`)}
                    >
                      Publicar
                    </button>
                  ))}
              </span>
            </div>
            {cfgOpen && (
              <div className="svc-form">
                {(a.params ?? []).map((p) => (
                  <label key={p.key}>
                    {p.label}
                    <input
                      type={p.type === "password" ? "password" : p.type === "int" ? "number" : "text"}
                      value={formVals[p.key] ?? ""}
                      placeholder={p.default || "(por defecto)"}
                      onChange={(e) => setFormVals((v) => ({ ...v, [p.key]: e.target.value }))}
                    />
                  </label>
                ))}
                <div className="svc-form-actions">
                  <button className="btn primary" onClick={() => install(a, formVals, "actualizando")}>
                    Aplicar cambios
                  </button>
                  <button className="btn" onClick={() => setOpenForm(null)}>
                    Cancelar
                  </button>
                </div>
                <div className="svc-hint">
                  Solo se tocan los valores vetados del catálogo; el resto de la
                  configuración se conserva (helm upgrade).
                </div>
              </div>
            )}
            {pubOpen && a.access && (
              <PublishForm
                cluster={cluster}
                name={a.name}
                namespace={a.namespace}
                service={a.access.service}
                port={a.access.port}
                onNotice={(t, tone) => setFb({ text: t, tone })}
                onDone={() => setOpenForm(null)}
              />
            )}
            {(cfgOpen || pubOpen) && a.access?.hint && <div className="svc-hint">{a.access.hint}</div>}
          </div>
        );
      })}

      {/* ---- rutas publicadas fuera del catálogo (también se adoptan) ---- */}
      {otherRoutes.map((i) => {
        const w = (cluster.snapshot?.workloads ?? []).find(
          (w) => w.namespace === i.namespace && w.name.includes(i.service),
        );
        return (
          <div className="svc-row" key={`${i.namespace}/${i.name}/${i.host}`}>
            <div className="svc-line">
              <span
                className="audit-dot"
                style={{ background: w ? (w.replicas > 0 ? "var(--good)" : "#F0932B") : "var(--faint)" }}
              />
              <div className="svc-main">
                <div className="svc-name">
                  {i.service}
                  <span className="svc-chip">ruta</span>
                </div>
                <div className="svc-meta">
                  {i.namespace}/{i.service}:{i.port} ·{" "}
                  <a className="svc-url" href={urlOf(i)} target="_blank" rel="noreferrer">
                    {i.host}
                  </a>
                </div>
              </div>
              <a className="bar-btn svc-open" href={urlOf(i)} target="_blank" rel="noreferrer">
                Abrir ↗
              </a>
            </div>
          </div>
        );
      })}

      {fb && <div className={`svc-notice ${fb.tone}`}>{fb.text}</div>}

      {/* ---- catálogo: instalar todo desde aquí ---- */}
      <button className="svc-catalog-toggle" onClick={() => setShowCatalog((v) => !v)}>
        {showCatalog ? "▾" : "▸"} Catálogo ({notInstalled.length} disponible(s))
      </button>
      {showCatalog &&
        CATEGORIES.map((cat) => {
          const items = notInstalled.filter((a) => a.category === cat.key);
          if (items.length === 0) return null;
          return (
            <div className="svc-cat" key={cat.key}>
              <div className="svc-cat-label">{cat.label}</div>
              {items.map((a) => {
                const cfgOpen = openForm === `cfg:${a.key}`;
                return (
                  <div className="svc-row" key={a.key}>
                    <div className="svc-line">
                      <span className="audit-dot" style={{ background: "var(--faint)" }} />
                      <div className="svc-main">
                        <div className="svc-name">{a.name}</div>
                        <div className="svc-meta">{a.description}</div>
                      </div>
                      <button
                        className="bar-btn"
                        onClick={() =>
                          (a.params?.length ?? 0) > 0 ? openConfig(a) : install(a, {}, "instalando")
                        }
                        disabled={busyKey !== null || !cluster.online}
                      >
                        {busyKey === a.key ? "instalando…" : "Instalar"}
                      </button>
                    </div>
                    {cfgOpen && (
                      <div className="svc-form">
                        {(a.params ?? []).map((p) => (
                          <label key={p.key}>
                            {p.label}
                            <input
                              type={p.type === "password" ? "password" : p.type === "int" ? "number" : "text"}
                              value={formVals[p.key] ?? ""}
                              placeholder={p.default || "(por defecto)"}
                              onChange={(e) => setFormVals((v) => ({ ...v, [p.key]: e.target.value }))}
                            />
                          </label>
                        ))}
                        <div className="svc-form-actions">
                          <button className="btn primary" onClick={() => install(a, formVals, "instalando")}>
                            Instalar {a.name}
                          </button>
                          <button className="btn" onClick={() => setOpenForm(null)}>
                            Cancelar
                          </button>
                        </div>
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          );
        })}
      {showCatalog && (
        <div className="svc-hint">
          Catálogo cerrado y con versión fijada — el agente nunca aplica YAML
          arbitrario. Instalar requiere el RBAC opt-in (agent-addons.yaml).
        </div>
      )}
    </div>
  );
}

/** Formulario de publicación: host + clase de ingress + TLS. Encola 'expose';
 *  cuando el agente crea el Ingress, la URL y el botón Abrir aparecen solos. */
function PublishForm({
  cluster,
  name,
  namespace,
  service,
  port,
  onNotice,
  onDone,
}: {
  cluster: ClusterView;
  name: string;
  namespace: string;
  service: string;
  port: number;
  onNotice: (msg: string, tone: Feedback["tone"]) => void;
  onDone: () => void;
}) {
  // Sugerencia: grafana.<dominio-de-atlas> (mismo DNS/proxy que ya usa Atlas).
  const base = window.location.hostname.split(".").slice(1).join(".");
  const suggested = `${name.toLowerCase().replace(/[^a-z0-9-]+/g, "-")}.${base || "example.com"}`;
  const [host, setHost] = useState(suggested);
  const [klass, setKlass] = useState("nginx");
  const [tls, setTls] = useState(window.location.protocol === "https:");
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      await postAction(cluster.clusterId, {
        kind: "expose",
        expose: { namespace, service, port, host, ingressClass: klass, tls },
      });
      onNotice(
        `publicando ${host}… en unos segundos aparecerá la URL (apunta el DNS de ${host} al Ingress)`,
        "info",
      );
      onDone();
    } catch (ex) {
      onNotice(`error: ${ex instanceof Error ? ex.message : String(ex)}`, "err");
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="svc-form" onSubmit={submit}>
      <label>
        dominio
        <input value={host} onChange={(e) => setHost(e.target.value)} />
      </label>
      <label>
        clase de ingress
        <select value={klass} onChange={(e) => setKlass(e.target.value)}>
          <option value="nginx">nginx</option>
          <option value="traefik">traefik (k3s/k3d)</option>
        </select>
      </label>
      <label className="svc-check">
        <input type="checkbox" checked={tls} onChange={(e) => setTls(e.target.checked)} />
        HTTPS con cert-manager (letsencrypt-production)
      </label>
      <button className="btn primary" type="submit" disabled={busy || !host}>
        {busy ? "publicando…" : "Publicar servicio"}
      </button>
    </form>
  );
}
