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

import { fetchTopology, type Topology, type Workload } from "./api";
import { ServiceNode, type ServiceNodeData } from "./ServiceNode";
import { providerColor, providerLabel, type IconKey } from "./icons";
import { layout, sizeFor, type LayoutEdge, type NodeSize } from "./layout";

const nodeTypes = { service: ServiceNode };
const POLL_MS = 5000;

// Namespaces del sistema: se muestran atenuados para no tapar tus apps.
const SYSTEM_NS = new Set([
  "kube-system",
  "kube-node-lease",
  "kube-public",
  "local-path-storage",
  "cilium-secrets",
]);

const LINK_COLOR = "#F0932B"; // naranja: conexiones observadas (Hubble)
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
  nodes: Node<ServiceNodeData>[];
  edges: Edge[];
  layoutEdges: LayoutEdge[];
  sizes: Map<string, NodeSize>;
}

/** Convierte la topología del backend en nodos/aristas (sin posicionar todavía). */
function build(topo: Topology | null): Built {
  const nodes: Node<ServiceNodeData>[] = [];
  const edges: Edge[] = [];
  const layoutEdges: LayoutEdge[] = [];
  const sizes = new Map<string, NodeSize>();

  const add = (
    id: string,
    data: ServiceNodeData,
  ): Node<ServiceNodeData> => {
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

    add(clusterId, {
      label: c.name,
      sublabel: providerLabel[c.provider] ?? c.provider,
      color,
      icon: "cluster",
      online: c.online,
      muted: !c.online,
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

    // Nodos (máquinas): control-plane + workers. Un servidor = un nodo.
    allNodes.forEach((n, j) => {
      const nid = `${clusterId}-node-${j}`;
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
      add(id, {
        label: w.name,
        sublabel: `${w.kind} · ${w.replicas}`,
        color: workloadColor(w),
        icon: workloadIcon(w),
        muted: !c.online || SYSTEM_NS.has(w.namespace),
      });
      // Pertenencia al clúster (arista muy tenue) + jerarquía de layout.
      edges.push({
        id: `e-belong-${id}`,
        source: clusterId,
        target: id,
        style: { stroke: color, opacity: 0.12 },
      });
      layoutEdges.push({ source: layoutParent, target: id });
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
  const [error, setError] = useState<string | null>(null);
  const instance = useRef<ReactFlowInstance | null>(null);

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
    const id = setInterval(load, POLL_MS);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, []);

  const { nodes, edges } = useMemo(() => {
    const b = build(topo);
    return { nodes: layout(b.nodes, b.layoutEdges, b.sizes, "LR"), edges: b.edges };
  }, [topo]);

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

  return (
    <div className="map-wrap">
      <div className="map-bar">
        <strong>Topología global</strong>
        <span className="badge">{clusterCount} clúster(es)</span>
        {error && <span className="err">sin conexión al control plane</span>}
        {!error && clusterCount === 0 && (
          <span className="hint">esperando el primer agente…</span>
        )}
      </div>
      <div className="map-canvas">
        <ReactFlow
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          onInit={(inst) => (instance.current = inst)}
          fitView
          minZoom={0.2}
          proOptions={{ hideAttribution: true }}
        >
          <Background gap={28} />
          <Controls />
        </ReactFlow>
      </div>
    </div>
  );
}
