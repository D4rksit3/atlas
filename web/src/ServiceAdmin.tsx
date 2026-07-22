// Vista "Administrar": un servicio instalado se USA y se ADMINISTRA dentro de
// Atlas, sin salir a otra página. A la izquierda: estado y operación de sus
// cargas, configuración (valores de Helm) y publicación. Al centro: la interfaz
// del servicio EMBEBIDA (iframe de su URL publicada).
import { useEffect, useState, type FormEvent } from "react";
import {
  fetchActions,
  postAction,
  type AddonInfo,
  type ClusterView,
  type IngressInfo,
  type Workload,
} from "./api";

interface Feedback {
  text: string;
  tone: "info" | "ok" | "err";
}

export interface AdminTarget {
  cluster: ClusterView;
  name: string; // nombre legible (Grafana, Argo CD, demo-web…)
  namespace: string; // namespace del servicio
  addon: AddonInfo | null; // entrada del catálogo, si la tiene
  service: string | null; // Service de su UI (para publicar)
  port: number;
  ingress: IngressInfo | null; // ruta publicada, si existe
}

function urlOf(i: IngressInfo): string {
  return `${i.tls ? "https" : "http"}://${i.host}`;
}

/** Sigue una acción hasta done/error. */
function track(clusterId: string, id: string, cb: (ok: boolean, err?: string) => void) {
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

export function ServiceAdmin({ target, onClose }: { target: AdminTarget; onClose: () => void }) {
  const { cluster, addon, ingress } = target;
  const [fb, setFb] = useState<Feedback | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState<"uninstall" | "unexpose" | null>(null);
  const [formVals, setFormVals] = useState<Record<string, string>>(() => {
    const defs: Record<string, string> = {};
    for (const p of addon?.params ?? []) defs[p.key] = p.default ?? "";
    return defs;
  });

  // Lo configurado, configurado queda: precarga el formulario con los valores
  // REALMENTE aplicados en la última instalación/actualización (del historial de
  // acciones; las contraseñas llegan enmascaradas y se dejan vacías = conservar).
  useEffect(() => {
    if (!addon || (addon.params?.length ?? 0) === 0) return;
    let alive = true;
    (async () => {
      try {
        const acts = await fetchActions(cluster.clusterId);
        const last = acts.find(
          (a) => a.kind === "install" && a.addon === addon.key && a.status === "done" && a.values,
        );
        if (!alive || !last?.values) return;
        setFormVals((cur) => {
          const next = { ...cur };
          for (const p of addon.params ?? []) {
            const v = last.values![p.key];
            if (v !== undefined && v !== "••••••") next[p.key] = v;
            if (p.type === "password") next[p.key] = ""; // vacío = se conserva
          }
          return next;
        });
      } catch {
        /* sin historial: se quedan los defaults */
      }
    })();
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster.clusterId, addon?.key]);

  const workloads = (cluster.snapshot?.workloads ?? []).filter(
    (w) => w.namespace === target.namespace,
  );
  const url = ingress ? urlOf(ingress) : null;
  // Contenido mixto: la GUI en https no puede embeber una URL http (el
  // navegador lo bloquea). Hay que publicar el servicio con TLS.
  const mixed = url !== null && window.location.protocol === "https:" && url.startsWith("http://");

  async function run(desc: string, req: Parameters<typeof postAction>[1]) {
    setBusy(true);
    setFb({ text: `${desc}…`, tone: "info" });
    try {
      const a = await postAction(cluster.clusterId, req);
      track(cluster.clusterId, a.id, (ok, err) => {
        setBusy(false);
        setFb(ok ? { text: `${desc} ✓`, tone: "ok" } : { text: `error: ${err}`, tone: "err" });
      });
    } catch (e) {
      setBusy(false);
      setFb({ text: `no se pudo encolar: ${e instanceof Error ? e.message : String(e)}`, tone: "err" });
    }
  }

  const canOp = (w: Workload) =>
    cluster.online && (w.kind === "Deployment" || w.kind === "StatefulSet");

  return (
    <div className="svcadmin">
      <div className="svcadmin-head">
        <button className="bar-btn" onClick={onClose}>‹ Servicios</button>
        <span
          className="audit-dot"
          style={{ background: cluster.online ? "var(--good)" : "#ff7c7c" }}
        />
        <span className="svcadmin-title">{target.name}</span>
        <span className="svcadmin-sub">
          {cluster.name} · {target.namespace}
        </span>
        {url && (
          <a className="svc-url svcadmin-url" href={url} target="_blank" rel="noreferrer">
            {ingress!.host} ↗
          </a>
        )}
        <button className="insp-x" onClick={onClose} aria-label="cerrar">×</button>
      </div>

      <div className="svcadmin-body">
        <div className="svcadmin-side">
          {/* ---- estado + operar cargas ---- */}
          <div className="insp-section">Cargas ({workloads.length})</div>
          {workloads.length === 0 && <div className="insp-hint-sm">sin cargas visibles aún</div>}
          {workloads.map((w) => (
            <div className="svcadmin-wl" key={`${w.kind}/${w.name}`}>
              <span
                className="audit-dot"
                style={{ background: w.replicas > 0 ? "var(--good)" : "#F0932B" }}
              />
              <div className="svcadmin-wl-meta">
                <span className="svcadmin-wl-name">{w.name}</span>
                <span className="svcadmin-wl-sub">{w.kind} · {w.replicas} réplica(s)</span>
              </div>
              <span className="svcadmin-wl-btns">
                <button
                  title="quitar una réplica"
                  disabled={busy || !canOp(w) || w.replicas === 0}
                  onClick={() =>
                    run(`escalar ${w.name} a ${w.replicas - 1}`, {
                      kind: "scale", namespace: w.namespace, workload: w.name,
                      workloadKind: w.kind, replicas: w.replicas - 1,
                    })
                  }
                >−</button>
                <button
                  title="añadir una réplica"
                  disabled={busy || !canOp(w)}
                  onClick={() =>
                    run(`escalar ${w.name} a ${w.replicas + 1}`, {
                      kind: "scale", namespace: w.namespace, workload: w.name,
                      workloadKind: w.kind, replicas: w.replicas + 1,
                    })
                  }
                >+</button>
                <button
                  title="reinicio suave (rollout)"
                  disabled={busy || !canOp(w)}
                  onClick={() =>
                    run(`reiniciar ${w.name}`, {
                      kind: "restart", namespace: w.namespace, workload: w.name,
                      workloadKind: w.kind, replicas: w.replicas,
                    })
                  }
                >⟳</button>
              </span>
            </div>
          ))}

          {/* ---- configurar (valores de Helm) ---- */}
          {addon && (addon.params?.length ?? 0) > 0 && (
            <>
              <div className="insp-section">Configurar</div>
              {(addon.params ?? []).map((p) => (
                <label className="svcadmin-field" key={p.key}>
                  {p.label}
                  <input
                    type={p.type === "password" ? "password" : p.type === "int" ? "number" : "text"}
                    value={formVals[p.key] ?? ""}
                    placeholder={p.type === "password" ? "(se conserva la actual)" : p.default || "(por defecto)"}
                    onChange={(e) => setFormVals((v) => ({ ...v, [p.key]: e.target.value }))}
                  />
                </label>
              ))}
              <button
                className="btn primary svcadmin-apply"
                disabled={busy || !cluster.online}
                onClick={() =>
                  run(`actualizando ${addon.name}`, {
                    kind: "install", addon: addon.key,
                    values: Object.fromEntries(Object.entries(formVals).filter(([, v]) => v !== "")),
                  })
                }
              >
                Aplicar cambios
              </button>
              <div className="insp-hint-sm">
                Solo los valores vetados del catálogo; el resto se conserva (helm upgrade).
              </div>
            </>
          )}

          {/* ---- publicación ---- */}
          <div className="insp-section">Publicación</div>
          {ingress ? (
            <div className="insp-hint-sm">
              Publicado en <span className="mono">{ingress.host}</span>
              {ingress.tls ? " (https)" : " (http)"} · clase{" "}
              <span className="mono">{ingress.class || "?"}</span>
              {mixed && (
                <div className="svcadmin-warn">
                  Atlas va por https y este servicio por http: el navegador
                  bloquea embeberlo. Publícalo con TLS para verlo aquí.
                </div>
              )}
              {ingress.name.startsWith("atlas-") && (
                <div className="svcadmin-danger-row">
                  {confirm === "unexpose" ? (
                    <>
                      <span>¿retirar {ingress.host}?</span>
                      <button
                        className="btn danger"
                        disabled={busy}
                        onClick={() => {
                          setConfirm(null);
                          run(`despublicando ${target.name}`, {
                            kind: "unexpose",
                            expose: {
                              namespace: target.namespace,
                              service: target.service!,
                              port: target.port,
                              host: ingress.host,
                            },
                          });
                        }}
                      >
                        Sí, despublicar
                      </button>
                      <button className="btn" onClick={() => setConfirm(null)}>No</button>
                    </>
                  ) : (
                    <button className="btn danger-ghost" onClick={() => setConfirm("unexpose")}>
                      Despublicar
                    </button>
                  )}
                </div>
              )}
            </div>
          ) : target.service ? (
            <AdminPublish target={target} onNotice={(t, tone) => setFb({ text: t, tone })} />
          ) : (
            <div className="insp-hint-sm">este servicio no declara una interfaz publicable</div>
          )}

          {addon?.access?.hint && (
            <>
              <div className="insp-section">Credenciales</div>
              <div className="insp-hint-sm svcadmin-hint">{addon.access.hint}</div>
            </>
          )}

          {/* ---- zona de peligro: desinstalar el complemento ---- */}
          {addon && (
            <>
              <div className="insp-section">Quitar</div>
              {confirm === "uninstall" ? (
                <div className="svcadmin-danger-row svcadmin-danger-confirm">
                  <span>
                    Se desinstala <b>{addon.name}</b> y se borra su namespace{" "}
                    <span className="mono">{addon.namespace}</span>. ¿Seguro?
                  </span>
                  <button
                    className="btn danger"
                    disabled={busy}
                    onClick={() => {
                      setConfirm(null);
                      run(`desinstalando ${addon.name}`, { kind: "uninstall", addon: addon.key });
                    }}
                  >
                    Sí, desinstalar
                  </button>
                  <button className="btn" onClick={() => setConfirm(null)}>Cancelar</button>
                </div>
              ) : (
                <div className="svcadmin-danger-row">
                  <button
                    className="btn danger-ghost"
                    disabled={busy || !cluster.online}
                    onClick={() => setConfirm("uninstall")}
                  >
                    Desinstalar {addon.name}
                  </button>
                </div>
              )}
            </>
          )}

          {fb && <div className={`svc-notice ${fb.tone}`}>{fb.text}</div>}
        </div>

        {/* ---- la interfaz del servicio, embebida ---- */}
        <div className="svcadmin-main">
          {url && !mixed ? (
            <>
              <iframe className="svcadmin-frame" src={url} title={target.name} />
              <div className="svcadmin-framenote">
                ¿No carga? El servicio puede no permitir embeberse —{" "}
                <a className="svc-url" href={url} target="_blank" rel="noreferrer">
                  ábrelo en una pestaña ↗
                </a>
              </div>
            </>
          ) : (
            <div className="svcadmin-empty">
              {mixed ? (
                <>
                  <div className="svcadmin-empty-title">Embebido bloqueado (contenido mixto)</div>
                  <div>
                    Publica <b>{target.name}</b> con TLS (o ponlo tras tu proxy https)
                    para usarlo aquí dentro. Mientras tanto:{" "}
                    <a className="svc-url" href={url!} target="_blank" rel="noreferrer">
                      abrir en una pestaña ↗
                    </a>
                  </div>
                </>
              ) : (
                <>
                  <div className="svcadmin-empty-title">Aún sin URL</div>
                  <div>
                    Publica <b>{target.name}</b> (panel de la izquierda) y su
                    interfaz aparecerá embebida aquí.
                  </div>
                </>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

/** Publicar desde la vista Administrar (host + clase + TLS). */
function AdminPublish({
  target,
  onNotice,
}: {
  target: AdminTarget;
  onNotice: (msg: string, tone: Feedback["tone"]) => void;
}) {
  const base = window.location.hostname.split(".").slice(1).join(".");
  const suggested = `${target.name.toLowerCase().replace(/[^a-z0-9-]+/g, "-")}.${base || "example.com"}`;
  const [host, setHost] = useState(suggested);
  const [klass, setKlass] = useState("nginx");
  const [tls, setTls] = useState(window.location.protocol === "https:");
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      await postAction(target.cluster.clusterId, {
        kind: "expose",
        expose: {
          namespace: target.namespace,
          service: target.service!,
          port: target.port,
          host,
          ingressClass: klass,
          tls,
        },
      });
      onNotice(`publicando ${host}… al crearse el Ingress, la interfaz aparecerá embebida`, "info");
    } catch (ex) {
      onNotice(`error: ${ex instanceof Error ? ex.message : String(ex)}`, "err");
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="svc-form svcadmin-pub" onSubmit={submit}>
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
        HTTPS con cert-manager
      </label>
      <button className="btn primary" type="submit" disabled={busy || !host}>
        {busy ? "publicando…" : "Publicar"}
      </button>
    </form>
  );
}
