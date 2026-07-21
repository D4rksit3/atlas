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
  type Topology,
  type Workload,
  type Annotation,
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
import { NodeGroup, type NodeGroupItem } from "./NodeGroup";

const nodeTypes = { service: ServiceNode, nodegroup: NodeGroup };
const POLL_MS = 5000;
type ViewMode = "flow" | "node";

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

function workloadIcon(w: Workload): IconKey {
  return w.kind === "StatefulSet" ? "database" : "workload";
}

function workloadColor(w: Workload): string {
  if (SYSTEM_NS.has(w.namespace)) return "#5A6577"; // sistema, atenuado
  if (w.kind === "StatefulSet") return "#E7476B"; // datos
  return "#2D74DA"; // apps
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
    const argocdInstalled = workloads.some(
      (w) => w.namespace === "argocd" && w.name === "argocd-server",
    );
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
        cluster: { clusterId: c.clusterId, online: c.online, argocd: argocdInstalled },
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
            sel: { key: wKey, title: w.name, kind: w.kind, subtitle: w.namespace, op },
          });
        });
        items.sort((a, b) => Number(a.muted) - Number(b.muted)); // apps primero
        nodes.push({
          id: nid, type: "nodegroup", position: { x: 0, y: 0 },
          data: {
            nodeName: n.name, role: n.role, online: n.ready && c.online,
            color: isCP ? CP_NODE : WORKER, items, onSelect: opts.onSelect,
          },
        });
        sizes.set(nid, { width: 258, height: 46 + Math.max(items.length, 1) * 28 + 12 });
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
      add(nid, {
        label: n.name,
        sublabel: n.role,
        color: isCP ? CP_NODE : WORKER,
        icon: "server",
        online: n.ready,
        muted: !c.online,
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
        sublabel: `${w.kind} · ${w.replicas}`,
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
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<Selection | null>(null);
  const [showAudit, setShowAudit] = useState(false);
  const [view, setView] = useState<ViewMode>("flow");
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
    load();
    loadAnnos();
    const id = setInterval(load, POLL_MS);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, []);

  const { nodes, edges } = useMemo(() => {
    const b = build(topo, annos, { view, onSelect: setSelected });
    return { nodes: layout(b.nodes, b.layoutEdges, b.sizes, "LR"), edges: b.edges };
  }, [topo, annos, view]);

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
        </div>
        <button
          className={`bar-btn${showAudit ? " active" : ""}`}
          onClick={() => setShowAudit((v) => !v)}
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
            onClose={() => setSelected(null)}
            onSaved={loadAnnos}
          />
        )}
        {showAudit && <AuditPanel onClose={() => setShowAudit(false)} />}
      </div>
    </div>
  );
}
