// Panel de Servicios: todo lo instalado en tus clústeres, adoptado en un solo
// lugar. Cada servicio con interfaz (Grafana, Argo CD…) aparece con su estado y
// se puede PUBLICAR (crear su Ingress) y ABRIR desde aquí, sin tocar kubectl.
// También lista cualquier ruta ya publicada en el clúster, la creara Atlas o no.
import { useEffect, useMemo, useState, type FormEvent } from "react";
import {
  fetchAddons,
  fetchTopology,
  postAction,
  type AddonInfo,
  type ClusterView,
  type IngressInfo,
  type Topology,
} from "./api";

const POLL_MS = 5000;

/** Una fila del panel: un servicio de un clúster, con o sin URL publicada. */
interface Row {
  cluster: ClusterView;
  key: string; // única en el panel
  name: string; // nombre legible
  namespace: string;
  service: string;
  port: number;
  running: boolean | null; // null = no sabemos (sin workload que mirar)
  ingress: IngressInfo | null; // ruta publicada, si existe
  hint?: string;
  fromAtlas: boolean; // viene del catálogo de complementos
}

/** Construye las filas: complementos instalados con UI + rutas ya publicadas. */
function buildRows(topo: Topology | null, addons: AddonInfo[]): Row[] {
  const rows: Row[] = [];
  for (const c of topo?.clusters ?? []) {
    const workloads = c.snapshot?.workloads ?? [];
    const ingresses = c.snapshot?.ingresses ?? [];
    const covered = new Set<string>(); // "ns/service" ya listados como complemento

    for (const a of addons) {
      if (!a.access) continue;
      const detect = workloads.find(
        (w) => w.namespace === a.namespace && w.name.includes(a.detectWorkload),
      );
      if (!detect) continue; // no instalado en este clúster
      const ing =
        ingresses.find(
          (i) => i.namespace === a.namespace && i.service === a.access!.service,
        ) ?? null;
      covered.add(`${a.namespace}/${a.access.service}`);
      rows.push({
        cluster: c,
        key: `${c.clusterId}:${a.key}`,
        name: a.name,
        namespace: a.namespace,
        service: a.access.service,
        port: a.access.port,
        running: detect.replicas > 0,
        ingress: ing,
        hint: a.access.hint,
        fromAtlas: true,
      });
    }

    // Rutas publicadas que no corresponden a un complemento del catálogo
    // (p. ej. servicios propios): también se adoptan en el panel.
    for (const i of ingresses) {
      if (covered.has(`${i.namespace}/${i.service}`)) continue;
      if (i.namespace === "atlas-system") continue; // la propia GUI
      const w = workloads.find(
        (w) => w.namespace === i.namespace && w.name.includes(i.service),
      );
      rows.push({
        cluster: c,
        key: `${c.clusterId}:${i.namespace}/${i.name}/${i.host}`,
        name: i.service,
        namespace: i.namespace,
        service: i.service,
        port: i.port,
        running: w ? w.replicas > 0 : null,
        ingress: i,
        fromAtlas: false,
      });
    }
  }
  return rows;
}

function urlOf(i: IngressInfo): string {
  return `${i.tls ? "https" : "http"}://${i.host}`;
}

export function ServicesPanel({ onClose }: { onClose: () => void }) {
  const [topo, setTopo] = useState<Topology | null>(null);
  const [addons, setAddons] = useState<AddonInfo[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [publishing, setPublishing] = useState<string | null>(null); // key de la fila con el form abierto
  const [notice, setNotice] = useState<Record<string, string>>({}); // key -> mensaje

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

  const rows = useMemo(() => buildRows(topo, addons), [topo, addons]);

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
        {!err && rows.length === 0 && (
          <div className="audit-empty">
            aún no hay servicios con interfaz instalados — instala Grafana o
            Argo CD desde el catálogo del mapa
          </div>
        )}
        {rows.map((r) => (
          <ServiceRow
            key={r.key}
            row={r}
            open={publishing === r.key}
            notice={notice[r.key]}
            onToggle={() => setPublishing(publishing === r.key ? null : r.key)}
            onNotice={(msg) => setNotice((n) => ({ ...n, [r.key]: msg }))}
          />
        ))}
      </div>
    </aside>
  );
}

function ServiceRow({
  row,
  open,
  notice,
  onToggle,
  onNotice,
}: {
  row: Row;
  open: boolean;
  notice?: string;
  onToggle: () => void;
  onNotice: (msg: string) => void;
}) {
  return (
    <div className="svc-row">
      <div className="svc-line">
        <span
          className="audit-dot"
          style={{
            background:
              row.running === null
                ? "var(--faint)"
                : row.running
                  ? "var(--good)"
                  : "#ff7c7c",
          }}
        />
        <div className="svc-main">
          <div className="svc-name">
            {row.name}
            <span className="svc-cluster"> · {row.cluster.name}</span>
          </div>
          <div className="svc-meta">
            {row.namespace}/{row.service}:{row.port}
            {row.ingress && (
              <>
                {" · "}
                <a
                  className="svc-url"
                  href={urlOf(row.ingress)}
                  target="_blank"
                  rel="noreferrer"
                >
                  {row.ingress.host}
                </a>
              </>
            )}
          </div>
        </div>
        {row.ingress ? (
          <a
            className="bar-btn svc-open"
            href={urlOf(row.ingress)}
            target="_blank"
            rel="noreferrer"
          >
            Abrir ↗
          </a>
        ) : (
          <button className={`bar-btn${open ? " active" : ""}`} onClick={onToggle}>
            Publicar
          </button>
        )}
      </div>
      {row.hint && !row.ingress && open && (
        <div className="svc-hint">{row.hint}</div>
      )}
      {row.hint && row.ingress && <div className="svc-hint">{row.hint}</div>}
      {open && !row.ingress && <PublishForm row={row} onNotice={onNotice} onDone={onToggle} />}
      {notice && <div className="svc-notice">{notice}</div>}
    </div>
  );
}

/** Formulario de publicación: host + clase de ingress + TLS. Encola la acción
 *  'expose'; cuando el agente crea el Ingress, la URL aparece sola en la fila. */
function PublishForm({
  row,
  onNotice,
  onDone,
}: {
  row: Row;
  onNotice: (msg: string) => void;
  onDone: () => void;
}) {
  // Sugerencia: grafana.<dominio-de-atlas> (mismo DNS/proxy que ya usa Atlas).
  const base = window.location.hostname.split(".").slice(1).join(".");
  const suggested = `${row.name.toLowerCase().replace(/[^a-z0-9-]+/g, "-")}.${base || "example.com"}`;
  const [host, setHost] = useState(suggested);
  const [klass, setKlass] = useState("nginx");
  const [tls, setTls] = useState(window.location.protocol === "https:");
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      await postAction(row.cluster.clusterId, {
        kind: "expose",
        expose: {
          namespace: row.namespace,
          service: row.service,
          port: row.port,
          host,
          ingressClass: klass,
          tls,
        },
      });
      onNotice(
        `publicando ${host}… en unos segundos aparecerá la URL (apunta el DNS de ${host} al Ingress)`,
      );
      onDone();
    } catch (ex) {
      onNotice(`error: ${ex instanceof Error ? ex.message : String(ex)}`);
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
