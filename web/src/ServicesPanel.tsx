// Módulo de Servicios: TODO el ciclo de vida de los servicios del clúster en un
// solo apartado — instalar desde el catálogo, y ADMINISTRAR cada servicio sin
// salir de Atlas: la vista "Administrar" embebe su interfaz y concentra operar,
// configurar y publicar. También adopta las rutas publicadas por fuera de Atlas.
import { useEffect, useMemo, useState } from "react";
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
import { ServiceAdmin, type AdminTarget } from "./ServiceAdmin";

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

/** Identifica qué servicio está abierto en Administrar (se re-resuelve en cada
 *  poll para que la vista refleje réplicas/URL al momento). */
type AdminKey =
  | { kind: "addon"; clusterId: string; addonKey: string }
  | { kind: "route"; clusterId: string; namespace: string; ingressName: string; host: string };

export function ServicesPanel({ onClose }: { onClose: () => void }) {
  const [topo, setTopo] = useState<Topology | null>(null);
  const [addons, setAddons] = useState<AddonInfo[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [admin, setAdmin] = useState<AdminKey | null>(null);

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

  // Resuelve el objetivo de la vista Administrar con los datos MÁS recientes.
  const adminTarget: AdminTarget | null = useMemo(() => {
    if (!admin) return null;
    const c = clusters.find((c) => c.clusterId === admin.clusterId);
    if (!c) return null;
    const ingresses = c.snapshot?.ingresses ?? [];
    if (admin.kind === "addon") {
      const a = addons.find((x) => x.key === admin.addonKey);
      if (!a) return null;
      const ing = a.access
        ? (ingresses.find((i) => i.namespace === a.namespace && i.service === a.access!.service) ?? null)
        : null;
      return {
        cluster: c,
        name: a.name,
        namespace: a.namespace,
        addon: a,
        service: a.access?.service ?? null,
        port: a.access?.port ?? 0,
        ingress: ing,
      };
    }
    const ing =
      ingresses.find(
        (i) => i.namespace === admin.namespace && i.name === admin.ingressName && i.host === admin.host,
      ) ?? null;
    if (!ing) return null;
    return {
      cluster: c,
      name: ing.service,
      namespace: ing.namespace,
      addon: null,
      service: ing.service,
      port: ing.port,
      ingress: ing,
    };
  }, [admin, clusters, addons]);

  return (
    <>
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
            <ClusterServices
              key={c.clusterId}
              cluster={c}
              addons={addons}
              onAdmin={setAdmin}
            />
          ))}
        </div>
      </aside>
      {adminTarget && <ServiceAdmin target={adminTarget} onClose={() => setAdmin(null)} />}
    </>
  );
}

/** Todos los servicios de UN clúster: instalados + rutas + catálogo. */
function ClusterServices({
  cluster,
  addons,
  onAdmin,
}: {
  cluster: ClusterView;
  addons: AddonInfo[];
  onAdmin: (k: AdminKey) => void;
}) {
  const [busyKey, setBusyKey] = useState<string | null>(null);
  const [fb, setFb] = useState<Feedback | null>(null);
  const [justInstalled, setJustInstalled] = useState<string[]>([]);
  const [openForm, setOpenForm] = useState<string | null>(null); // addon del catálogo con form de valores
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
  const otherRoutes = ingresses.filter(
    (i) =>
      i.namespace !== "atlas-system" &&
      !addons.some((a) => a.access && a.namespace === i.namespace && a.access.service === i.service),
  );

  function openInstallForm(a: AddonInfo) {
    const defs: Record<string, string> = {};
    for (const p of a.params ?? []) defs[p.key] = p.default ?? "";
    setFormVals(defs);
    setOpenForm(openForm === a.key ? null : a.key);
  }

  async function install(a: AddonInfo, values: Record<string, string>) {
    setOpenForm(null);
    setBusyKey(a.key);
    setFb({ text: `instalando ${a.name}… (puede tardar unos minutos)`, tone: "info" });
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
          setFb({ text: `${a.name} instalado ✓ — ábrelo con Administrar`, tone: "ok" });
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

      {/* ---- instalados: TODO se administra dentro de Atlas ---- */}
      {installed.length === 0 && (
        <div className="audit-empty">
          nada instalado aún — abre el catálogo y despliega tu primer servicio
        </div>
      )}
      {installed.map(({ addon: a, workload: w }) => {
        const ing = ingressFor(a);
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
              <button
                className="btn primary svc-admin-btn"
                onClick={() => onAdmin({ kind: "addon", clusterId: cluster.clusterId, addonKey: a.key })}
              >
                Administrar
              </button>
            </div>
          </div>
        );
      })}

      {/* ---- rutas publicadas fuera del catálogo (también se administran) ---- */}
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
              <button
                className="btn primary svc-admin-btn"
                onClick={() =>
                  onAdmin({
                    kind: "route",
                    clusterId: cluster.clusterId,
                    namespace: i.namespace,
                    ingressName: i.name,
                    host: i.host,
                  })
                }
              >
                Administrar
              </button>
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
              {items.map((a) => (
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
                        (a.params?.length ?? 0) > 0 ? openInstallForm(a) : install(a, {})
                      }
                      disabled={busyKey !== null || !cluster.online}
                    >
                      {busyKey === a.key ? "instalando…" : "Instalar"}
                    </button>
                  </div>
                  {openForm === a.key && (
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
                        <button className="btn primary" onClick={() => install(a, formVals)}>
                          Instalar {a.name}
                        </button>
                        <button className="btn" onClick={() => setOpenForm(null)}>
                          Cancelar
                        </button>
                      </div>
                    </div>
                  )}
                </div>
              ))}
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
