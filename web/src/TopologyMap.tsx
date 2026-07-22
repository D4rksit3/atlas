// El mapa vivo: descarga la topología del control plane cada pocos segundos y la
// dibuja con React Flow. La DISPOSICIÓN la calcula dagre (ver layout.ts): consola
// → control plane → clúster → nodos → cargas, de izquierda a derecha. Las aristas
// que se pintan conservan su dirección semántica.
import { useEffect, useMemo, useRef, useState } from "react";
import ReactFlow, {
  Background,
  Controls,
  MarkerType,
  type Edge,
  type Node,
  type ReactFlowInstance,
} from "reactflow";
import "reactflow/dist/style.css";

import {
  fetchTopology,
  fetchAnnotations,
  fetchAddons,
  fetchAlerts,
  type Topology,
  type Workload,
  type Annotation,
  type App,
  type AddonInfo,
} from "./api";
import {
  ServiceNode,
  type ServiceNodeData,
  type Selection,
} from "./ServiceNode";
import { providerColor, providerLabel, type IconKey } from "./icons";
import { layout, sizeFor, type LayoutEdge, type NodeSize } from "./layout";
import { Inspector } from "./Inspector";
import { AuditPanel } from "./AuditPanel";
import { ServicesPanel } from "./ServicesPanel";
import { UsersPanel } from "./UsersPanel";
import { AlertsPanel } from "./AlertsPanel";
import { NodeGroup, type NodeGroupItem } from "./NodeGroup";
import { NetGroup, netGroupHeight, type NetGroupData, type NetServiceRow, type NetWorkloadRef } from "./NetGroup";

const nodeTypes = { service: ServiceNode, nodegroup: NodeGroup, netgroup: NetGroup };
const POLL_MS = 5000;
type ViewMode = "flow" | "node" | "red";

// Namespaces del sistema: se muestran atenuados para no tapar tus apps.
const SYSTEM_NS = new Set([
  "kube-system",
  "kube-node-lease",
  "kube-public",
  "local-path-storage",
  "cilium-secrets",
]);

const LINK_COLOR = "#F0932B"; // naranja: conexiones observadas (Hubble)
const PLACE_COLOR = "#16B5A5"; // teal: ubicación de pods (carga → nodo)
const CP_NODE = "#7C5CE6"; // violeta: nodo control-plane
const WORKER = "#0E9E6E"; // verde: nodo worker

/** Formatea consumo vivo: "123m · 456Mi". */
function fmtUsage(u?: { cpum: number; memMi: number } | null): string {
  return u ? `${u.cpum}m · ${u.memMi}Mi` : "";
}

function workloadIcon(w: Workload): IconKey {
  return w.kind === "StatefulSet" ? "database" : "workload";
}

function workloadColor(w: Workload): string {
  if (SYSTEM_NS.has(w.namespace)) return "#5A6577"; // sistema, atenuado
  if (w.kind === "StatefulSet") return "#E7476B"; // datos
  return "#2D74DA"; // apps
}

/** Color de un proyecto GitOps según su estado de sync/health. */
function appColor(app: App): string {
  if (app.health === "Degraded" || app.health === "Missing") return "#E7476B"; // problema
  if (app.sync === "OutOfSync" || app.health === "Progressing" || app.sync === "Unknown")
    return "#F0932B"; // pendiente/en curso
  return "#0E9E6E"; // Synced + Healthy
}

interface Built {
  nodes: Node[];
  edges: Edge[];
  layoutEdges: LayoutEdge[];
  sizes: Map<string, NodeSize>;
}

interface BuildOpts {
  view: ViewMode;
  onSelect: (s: Selection) => void;
}

/** Convierte la topología del backend en nodos/aristas (sin posicionar todavía).
 *  `annos` son los metadatos editables que se superponen (alias, color, nota).
 *  `opts.view`: "flow" (flujo por capas) | "node" (cargas agrupadas por nodo). */
function build(
  topo: Topology | null,
  annos: Record<string, Annotation>,
  addons: AddonInfo[],
  opts: BuildOpts,
): Built {
  const nodes: Node[] = [];
  const edges: Edge[] = [];
  const layoutEdges: LayoutEdge[] = [];
  const sizes = new Map<string, NodeSize>();

  const add = (id: string, data: ServiceNodeData): Node<ServiceNodeData> => {
    sizes.set(id, sizeFor(data.label, data.sublabel));
    const n: Node<ServiceNodeData> = {
      id,
      type: "service",
      position: { x: 0, y: 0 }, // dagre lo coloca después
      data,
    };
    nodes.push(n);
    return n;
  };

  // Consola (GUI) y control plane, siempre presentes.
  add("console", { label: "Consola · GUI", color: "#12B5A5", icon: "console" });
  add("control-plane", {
    label: "Control Plane",
    sublabel: "self-hosted",
    color: "#5B57E0",
    icon: "controlplane",
  });
  edges.push({
    id: "e-console-cp",
    source: "console",
    target: "control-plane",
    animated: true,
    markerEnd: { type: MarkerType.ArrowClosed },
  });
  layoutEdges.push({ source: "console", target: "control-plane" });

  const clusters = topo?.clusters ?? [];
  clusters.forEach((c) => {
    const color = providerColor[c.provider] ?? "#5A6577";
    const clusterId = `cluster-${c.clusterId}`;
    const allNodes = c.snapshot?.nodes ?? [];
    const workloads = c.snapshot?.workloads ?? [];

    const cAnno = annos[c.clusterId] ?? {};
    // Detección genérica de complementos: instalado si existe su carga señal.
    const installedAddons = addons
      .filter((a) =>
        workloads.some((w) => w.namespace === a.namespace && w.name.includes(a.detectWorkload)),
      )
      .map((a) => a.key);
    add(clusterId, {
      label: cAnno.displayName || c.name,
      sublabel: providerLabel[c.provider] ?? c.provider,
      color: cAnno.color || color,
      icon: "cluster",
      online: c.online,
      muted: !c.online,
      hasNote: !!cAnno.note,
      sel: {
        key: c.clusterId,
        title: c.name,
        kind: "Clúster",
        subtitle: providerLabel[c.provider] ?? c.provider,
        cluster: {
          clusterId: c.clusterId,
          online: c.online,
          installedAddons,
          apps: c.snapshot?.apps ?? [],
        },
      },
    });

    // El agente marca hacia casa -> arista del clúster al control plane (visual).
    edges.push({
      id: `e-${clusterId}-cp`,
      source: clusterId,
      target: "control-plane",
      animated: c.online,
      style: { stroke: color, opacity: c.online ? 1 : 0.35 },
      markerEnd: { type: MarkerType.ArrowClosed, color },
      label: c.online ? "mTLS" : "offline",
    });
    // ...pero para DISPONER, el clúster va DESPUÉS del control plane.
    layoutEdges.push({ source: "control-plane", target: clusterId });

    // ---- Vista "Red": la comunicación interna, ordenada por namespace ----
    // Cada caja es un namespace con la cadena legible: host publicado ->
    // Service (ClusterIP:puerto) -> cargas (con las IPs de sus pods). Nada de
    // marañas: los namespaces de sistema se omiten para que se vea claro.
    if (opts.view === "red") {
      const services = c.snapshot?.services ?? [];
      const ingresses = c.snapshot?.ingresses ?? [];
      const namespaces = Array.from(
        new Set([...workloads.map((w) => w.namespace), ...services.map((s) => s.namespace)]),
      )
        .filter((ns) => !SYSTEM_NS.has(ns))
        .sort();

      const wlRef = (w: Workload): NetWorkloadRef => {
        const wKey = `${c.clusterId}/${w.namespace}/${w.name}`;
        const wAnno = annos[wKey] ?? {};
        const op = {
          clusterId: c.clusterId, namespace: w.namespace, workload: w.name,
          workloadKind: w.kind, replicas: w.replicas, online: c.online,
        };
        const ips = (w.pods ?? []).map((p) => p.ip).filter((ip): ip is string => !!ip);
        return {
          key: wKey,
          label: wAnno.displayName || w.name,
          kind: w.kind,
          replicas: w.replicas,
          ips: ips.slice(0, 3).concat(ips.length > 3 ? [`+${ips.length - 3}`] : []),
          sel: {
            key: wKey, title: w.name, kind: w.kind, subtitle: w.namespace, op,
            pods: w.pods ?? [], usage: w.usage ?? undefined,
          },
        };
      };

      namespaces.forEach((ns, j) => {
        const nsWorkloads = workloads.filter((w) => w.namespace === ns);
        const nsServices = services.filter((s) => s.namespace === ns);
        const covered = new Set<string>();
        const rows: NetServiceRow[] = nsServices.map((s) => {
          const port = s.ports?.[0]?.port;
          const addr =
            s.type === "Headless" ? "headless"
            : s.clusterIP ? `${s.clusterIP}${port ? `:${port}` : ""}`
            : "sin ClusterIP";
          const wls = (s.workloads ?? [])
            .map((name) => nsWorkloads.find((w) => w.name === name))
            .filter((w): w is Workload => !!w);
          wls.forEach((w) => covered.add(w.name));
          return {
            key: `${ns}/${s.name}`,
            name: s.name,
            addr,
            hosts: ingresses.filter((i) => i.namespace === ns && i.service === s.name).map((i) => i.host),
            workloads: wls.map(wlRef),
          };
        });
        const orphans = nsWorkloads.filter((w) => !covered.has(w.name)).map(wlRef);

        const nid = `${clusterId}-net-${j}`;
        const data: NetGroupData = { namespace: ns, muted: !c.online, services: rows, orphans, onSelect: opts.onSelect };
        nodes.push({ id: nid, type: "netgroup", position: { x: 0, y: 0 }, data });
        sizes.set(nid, { width: 306, height: netGroupHeight(data) });
        edges.push({
          id: `e-${nid}`, source: clusterId, target: nid,
          style: { stroke: color, opacity: 0.4 },
        });
        layoutEdges.push({ source: clusterId, target: nid });
      });
      return; // fin de este clúster en vista red
    }

    // ---- Vista "por nodo": cada nodo es una caja con sus cargas dentro ----
    if (opts.view === "node") {
      allNodes.forEach((n, j) => {
        const nid = `${clusterId}-ng-${j}`;
        const isCP = n.role === "control-plane";
        const items: NodeGroupItem[] = [];
        workloads.forEach((w) => {
          const pl = (w.placement ?? []).find((p) => p.node === n.name);
          if (!pl) return; // esta carga no tiene pods en este nodo
          const wKey = `${c.clusterId}/${w.namespace}/${w.name}`;
          const wAnno = annos[wKey] ?? {};
          const op = {
            clusterId: c.clusterId, namespace: w.namespace, workload: w.name,
            workloadKind: w.kind, replicas: w.replicas, online: c.online,
          };
          items.push({
            key: `${wKey}@${n.name}`,
            label: wAnno.displayName || w.name,
            color: wAnno.color || workloadColor(w),
            icon: workloadIcon(w),
            pods: pl.pods,
            muted: SYSTEM_NS.has(w.namespace),
            sel: {
              key: wKey, title: w.name, kind: w.kind, subtitle: w.namespace, op,
              pods: w.pods ?? [], usage: w.usage ?? undefined,
            },
          });
        });
        items.sort((a, b) => Number(a.muted) - Number(b.muted)); // apps primero
        nodes.push({
          id: nid, type: "nodegroup", position: { x: 0, y: 0 },
          data: {
            nodeName: n.name, role: n.unschedulable ? `${n.role} · acordonado` : n.role,
            online: n.ready && c.online,
            color: isCP ? CP_NODE : WORKER, items, onSelect: opts.onSelect,
            usage: fmtUsage(n.usage),
            nodeSel: {
              key: `${c.clusterId}/node/${n.name}`, title: n.name, kind: "Nodo",
              subtitle: n.role, usage: n.usage ?? undefined,
              node: {
                clusterId: c.clusterId, name: n.name,
                online: c.online, unschedulable: !!n.unschedulable,
              },
            },
          },
        });
        // Altura acorde al CSS real (fila = 30px): si se subestima, las cajas
        // se solapan cuando el nodo tiene muchas cargas.
        sizes.set(nid, { width: 258, height: 46 + Math.max(items.length, 1) * 30 + 14 });
        edges.push({
          id: `e-${nid}`, source: clusterId, target: nid,
          style: { stroke: isCP ? CP_NODE : WORKER, opacity: 0.45 },
        });
        layoutEdges.push({ source: clusterId, target: nid });
      });
      return; // fin de este clúster en vista por nodo
    }

    // ---- Vista "flujo" (por defecto) ----
    // Nodos (máquinas): control-plane + workers. Un servidor = un nodo.
    const nodeIdByName = new Map<string, string>();
    allNodes.forEach((n, j) => {
      const nid = `${clusterId}-node-${j}`;
      nodeIdByName.set(n.name, nid);
      const isCP = n.role === "control-plane";
      const roleTxt = n.unschedulable ? `${n.role} · acordonado` : n.role;
      add(nid, {
        label: n.name,
        sublabel: n.usage ? `${roleTxt} · ${fmtUsage(n.usage)}` : roleTxt,
        color: isCP ? CP_NODE : WORKER,
        icon: "server",
        online: n.ready && !n.unschedulable,
        muted: !c.online,
        sel: {
          key: `${c.clusterId}/node/${n.name}`,
          title: n.name,
          kind: "Nodo",
          subtitle: roleTxt,
          usage: n.usage ?? undefined,
          node: {
            clusterId: c.clusterId, name: n.name,
            online: c.online, unschedulable: !!n.unschedulable,
          },
        },
      });
      edges.push({
        id: `e-${nid}`,
        source: clusterId,
        target: nid,
        style: { stroke: isCP ? CP_NODE : WORKER, opacity: 0.4 },
      });
      layoutEdges.push({ source: clusterId, target: nid });
    });

    // Cargas (servicios lógicos): pueden cruzar varios nodos, por eso van en su
    // propia columna. Para disponerlas una capa a la derecha, las colgamos del
    // primer nodo en el grafo de layout (no se dibuja esa arista).
    const layoutParent = allNodes.length ? `${clusterId}-node-0` : clusterId;
    const wlId = new Map<string, string>();
    workloads.forEach((w) => {
      const id = `${clusterId}-wl-${w.namespace}-${w.name}`;
      wlId.set(w.name, id);
      const wKey = `${c.clusterId}/${w.namespace}/${w.name}`;
      const wAnno = annos[wKey] ?? {};
      const op = {
        clusterId: c.clusterId,
        namespace: w.namespace,
        workload: w.name,
        workloadKind: w.kind,
        replicas: w.replicas,
        online: c.online,
      };
      add(id, {
        label: wAnno.displayName || w.name,
        sublabel: w.usage ? `${w.kind} · ${w.replicas} · ${fmtUsage(w.usage)}` : `${w.kind} · ${w.replicas}`,
        color: wAnno.color || workloadColor(w),
        icon: workloadIcon(w),
        muted: !c.online || SYSTEM_NS.has(w.namespace),
        hasNote: !!wAnno.note,
        op,
        sel: {
          key: wKey,
          title: w.name,
          kind: w.kind,
          subtitle: w.namespace,
          op,
          pods: w.pods ?? [],
          usage: w.usage ?? undefined,
        },
      });
      // Pertenencia al clúster (arista muy tenue) + jerarquía de layout.
      edges.push({
        id: `e-belong-${id}`,
        source: clusterId,
        target: id,
        style: { stroke: color, opacity: 0.12 },
      });
      layoutEdges.push({ source: layoutParent, target: id });

      // Ubicación: en qué nodos corren sus pods (solo apps, para no saturar).
      if (!SYSTEM_NS.has(w.namespace)) {
        (w.placement ?? []).forEach((pl) => {
          const nodeId = nodeIdByName.get(pl.node);
          if (!nodeId) return;
          edges.push({
            id: `e-place-${id}-${pl.node}`,
            source: id,
            target: nodeId,
            label: `×${pl.pods}`,
            labelBgPadding: [4, 2],
            labelStyle: { fill: PLACE_COLOR, fontSize: 10, fontWeight: 600 },
            labelBgStyle: { fill: "var(--panel, #131c2c)", fillOpacity: 0.85 },
            style: { stroke: PLACE_COLOR, strokeWidth: 1.2, strokeDasharray: "4 4", opacity: 0.55 },
          });
        });
      }
    });

    // Proyectos GitOps (Applications de ArgoCD) como nodos junto al clúster.
    (c.snapshot?.apps ?? []).forEach((app, j) => {
      const id = `${clusterId}-app-${j}`;
      const col = appColor(app);
      const aKey = `${c.clusterId}/${app.namespace}/${app.name}`;
      const aAnno = annos[aKey] ?? {};
      add(id, {
        label: aAnno.displayName || app.name,
        sublabel: `GitOps · ${app.sync}`,
        color: aAnno.color || col,
        icon: "gitops",
        online: c.online,
        muted: !c.online,
        hasNote: !!aAnno.note,
        sel: {
          key: aKey,
          title: app.name,
          kind: "Proyecto GitOps",
          subtitle: app.repoURL,
          app: {
            clusterId: c.clusterId,
            name: app.name,
            online: c.online,
            sync: app.sync,
            health: app.health,
            repoURL: app.repoURL,
            resources: app.resources ?? [],
          },
        },
      });
      edges.push({
        id: `e-${id}`,
        source: clusterId,
        target: id,
        style: { stroke: col, opacity: 0.55, strokeDasharray: "3 3" },
      });
      layoutEdges.push({ source: clusterId, target: id });
    });

    // Conexiones REALES entre servicios (observadas por Hubble).
    (c.snapshot?.links ?? []).forEach((l, j) => {
      const s = wlId.get(l.from);
      const t = wlId.get(l.to);
      if (!s || !t || s === t) return;
      edges.push({
        id: `e-link-${clusterId}-${j}`,
        source: s,
        target: t,
        animated: c.online,
        zIndex: 5,
        style: { stroke: LINK_COLOR, strokeWidth: 1.8 },
        markerEnd: { type: MarkerType.ArrowClosed, color: LINK_COLOR },
      });
    });
  });

  return { nodes, edges, layoutEdges, sizes };
}

export function TopologyMap() {
  const [topo, setTopo] = useState<Topology | null>(null);
  const [annos, setAnnos] = useState<Record<string, Annotation>>({});
  const [addons, setAddons] = useState<AddonInfo[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<Selection | null>(null);
  const [showAudit, setShowAudit] = useState(false);
  const [showServices, setShowServices] = useState(false);
  const [showUsers, setShowUsers] = useState(false);
  const [showAlerts, setShowAlerts] = useState(false);
  const [alertCount, setAlertCount] = useState<{ total: number; critical: number }>({ total: 0, critical: 0 });
  // La vista elegida se recuerda entre sesiones (lo configurado, configurado queda).
  const [view, setViewRaw] = useState<ViewMode>(() => {
    const v = localStorage.getItem("atlas.view");
    return v === "node" || v === "red" ? v : "flow";
  });
  const setView = (v: ViewMode) => {
    localStorage.setItem("atlas.view", v);
    setViewRaw(v);
  };
  const instance = useRef<ReactFlowInstance | null>(null);

  const loadAnnos = async () => {
    try {
      setAnnos(await fetchAnnotations());
    } catch {
      /* sin permiso o sin auth: seguimos sin anotaciones */
    }
  };

  useEffect(() => {
    let alive = true;
    const load = async () => {
      try {
        const t = await fetchTopology();
        if (alive) {
          setTopo(t);
          setError(null);
        }
      } catch (e) {
        if (alive) setError(String(e));
      }
    };
    const loadAlerts = async () => {
      try {
        const a = await fetchAlerts();
        if (alive) setAlertCount({ total: a.length, critical: a.filter((x) => x.severity === "critical").length });
      } catch {
        /* sin sesión o control plane viejo: sin badge */
      }
    };
    load();
    loadAnnos();
    loadAlerts();
    fetchAddons().then(setAddons).catch(() => {});
    const id = setInterval(() => { load(); loadAlerts(); }, POLL_MS);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, []);

  const { nodes, edges } = useMemo(() => {
    const b = build(topo, annos, addons, { view, onSelect: setSelected });
    return { nodes: layout(b.nodes, b.layoutEdges, b.sizes, "LR"), edges: b.edges };
  }, [topo, annos, addons, view]);

  const clusterCount = topo?.clusters?.length ?? 0;

  // Reencuadra solo cuando cambia el CONJUNTO de nodos (no en cada poll), para
  // no pelear con el zoom/paneo del usuario.
  const sig = nodes.map((n) => n.id).join("|");
  useEffect(() => {
    const t = setTimeout(() => {
      instance.current?.fitView({ padding: 0.16, duration: 400 });
    }, 50);
    return () => clearTimeout(t);
  }, [sig]);

  // Mantén fresca la carga seleccionada (réplicas/online) cuando llega topología
  // nueva, para que el Inspector refleje el resultado de una acción.
  useEffect(() => {
    if (!selected?.op || !topo) return;
    const op = selected.op;
    const c = topo.clusters?.find((c) => c.clusterId === op.clusterId);
    const w = c?.snapshot?.workloads?.find(
      (w) => w.namespace === op.namespace && w.name === op.workload,
    );
    if (c && w && (w.replicas !== op.replicas || c.online !== op.online)) {
      setSelected({ ...selected, op: { ...op, replicas: w.replicas, online: c.online } });
    }
  }, [topo, selected]);

  return (
    <div className="map-wrap">
      <div className="map-bar">
        <strong>Topología global</strong>
        <span className="badge">{clusterCount} clúster(es)</span>
        {error && <span className="err">sin conexión al control plane</span>}
        {!error && clusterCount === 0 && (
          <span className="hint">esperando el primer agente…</span>
        )}
        <div className="view-toggle" role="tablist" aria-label="vista del mapa">
          <button
            className={view === "flow" ? "on" : ""}
            onClick={() => setView("flow")}
          >
            Flujo
          </button>
          <button
            className={view === "node" ? "on" : ""}
            onClick={() => setView("node")}
          >
            Por nodo
          </button>
          <button
            className={view === "red" ? "on" : ""}
            onClick={() => setView("red")}
          >
            Red
          </button>
        </div>
        <button
          className={`bar-btn alerts-btn${showAlerts ? " active" : ""}${alertCount.critical > 0 ? " crit" : alertCount.total > 0 ? " warn" : ""}`}
          onClick={() => {
            setShowAlerts((v) => !v);
            setShowServices(false);
            setShowAudit(false);
            setShowUsers(false);
          }}
        >
          Alertas{alertCount.total > 0 ? ` · ${alertCount.total}` : ""}
        </button>
        <button
          className={`bar-btn${showServices ? " active" : ""}`}
          onClick={() => {
            setShowServices((v) => !v);
            setShowAudit(false);
            setShowUsers(false);
            setShowAlerts(false);
          }}
        >
          Servicios
        </button>
        <button
          className={`bar-btn${showUsers ? " active" : ""}`}
          onClick={() => {
            setShowUsers((v) => !v);
            setShowAudit(false);
            setShowServices(false);
            setShowAlerts(false);
          }}
        >
          Usuarios
        </button>
        <button
          className={`bar-btn${showAudit ? " active" : ""}`}
          onClick={() => {
            setShowAudit((v) => !v);
            setShowServices(false);
            setShowUsers(false);
            setShowAlerts(false);
          }}
        >
          Actividad
        </button>
      </div>
      <div className="map-canvas">
        <ReactFlow
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          onInit={(inst) => (instance.current = inst)}
          onNodeClick={(_, node) => {
            setSelected((node.data as ServiceNodeData).sel ?? null);
          }}
          onPaneClick={() => setSelected(null)}
          fitView
          minZoom={0.2}
          proOptions={{ hideAttribution: true }}
        >
          <Background gap={28} />
          <Controls />
        </ReactFlow>
        {selected && (
          <Inspector
            sel={selected}
            annotation={annos[selected.key] ?? {}}
            addons={addons}
            onClose={() => setSelected(null)}
            onSaved={loadAnnos}
          />
        )}
        {showAudit && <AuditPanel onClose={() => setShowAudit(false)} />}
        {showServices && <ServicesPanel onClose={() => setShowServices(false)} />}
        {showUsers && <UsersPanel onClose={() => setShowUsers(false)} />}
        {showAlerts && <AlertsPanel onClose={() => setShowAlerts(false)} />}
      </div>
    </div>
  );
}
