// El mapa vivo: descarga la topología del control plane cada pocos segundos y
// la dibuja con React Flow. Disposición por columnas (consola → control plane →
// clúster → nodos → cargas). Las conexiones entre cargas vienen de Hubble.
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

function workloadIcon(w: Workload): IconKey {
  return w.kind === "StatefulSet" ? "database" : "workload";
}

function workloadColor(w: Workload): string {
  if (SYSTEM_NS.has(w.namespace)) return "#5A6577"; // sistema, atenuado
  if (w.kind === "StatefulSet") return "#E7476B"; // datos
  return "#2D74DA"; // apps
}

/** Convierte la topología del backend en nodos/aristas de React Flow. */
function build(topo: Topology | null): {
  nodes: Node<ServiceNodeData>[];
  edges: Edge[];
} {
  const nodes: Node<ServiceNodeData>[] = [];
  const edges: Edge[] = [];

  // Consola (GUI) y control plane, siempre presentes.
  nodes.push({
    id: "console",
    type: "service",
    position: { x: 0, y: 260 },
    data: { label: "Consola · GUI", color: "#12B5A5", icon: "console" },
  });
  nodes.push({
    id: "control-plane",
    type: "service",
    position: { x: 320, y: 260 },
    data: {
      label: "Control Plane",
      sublabel: "self-hosted",
      color: "#5B57E0",
      icon: "controlplane",
    },
  });
  edges.push({
    id: "e-console-cp",
    source: "console",
    target: "control-plane",
    animated: true,
    markerEnd: { type: MarkerType.ArrowClosed },
  });

  const clusters = topo?.clusters ?? [];
  let yCursor = 0;
  clusters.forEach((c) => {
    const color = providerColor[c.provider] ?? "#5A6577";
    const clusterId = `cluster-${c.clusterId}`;
    const workers = (c.snapshot?.nodes ?? []).filter((n) => n.role === "worker");
    const workloads = c.snapshot?.workloads ?? [];

    // Alto de la banda de este clúster (para no solapar con el siguiente).
    const rows = Math.max(workers.length, workloads.length, 1);
    const band = rows * 82;
    const base = yCursor;
    const clusterY = base + band / 2 - 30;

    nodes.push({
      id: clusterId,
      type: "service",
      position: { x: 680, y: clusterY },
      data: {
        label: c.name,
        sublabel: providerLabel[c.provider] ?? c.provider,
        color,
        icon: "cluster",
        online: c.online,
        muted: !c.online,
      },
    });

    // El agente marca hacia casa -> arista del clúster al control plane.
    edges.push({
      id: `e-${clusterId}-cp`,
      source: clusterId,
      target: "control-plane",
      animated: c.online,
      style: { stroke: color, opacity: c.online ? 1 : 0.35 },
      markerEnd: { type: MarkerType.ArrowClosed, color },
      label: c.online ? "mTLS" : "offline",
    });

    // Nodos worker: columna intermedia.
    workers.forEach((n, j) => {
      const nid = `${clusterId}-node-${j}`;
      nodes.push({
        id: nid,
        type: "service",
        position: { x: 980, y: base + j * 82 },
        data: {
          label: n.name,
          color: "#0E9E6E",
          icon: "server",
          online: n.ready,
          muted: !c.online,
        },
      });
      edges.push({
        id: `e-${nid}`,
        source: clusterId,
        target: nid,
        style: { stroke: "#0E9E6E", opacity: 0.45 },
      });
    });

    // Cargas: columna derecha. Guardamos el id por nombre para tejer los enlaces.
    const wlId = new Map<string, string>();
    workloads.forEach((w, j) => {
      const id = `${clusterId}-wl-${w.namespace}-${w.name}`;
      wlId.set(w.name, id);
      nodes.push({
        id,
        type: "service",
        position: { x: 1360, y: base + j * 82 },
        data: {
          label: w.name,
          sublabel: `${w.kind} · ${w.replicas}`,
          color: workloadColor(w),
          icon: workloadIcon(w),
          muted: !c.online || SYSTEM_NS.has(w.namespace),
        },
      });
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
        style: { stroke: LINK_COLOR, strokeWidth: 1.8 },
        markerEnd: { type: MarkerType.ArrowClosed, color: LINK_COLOR },
      });
    });

    yCursor = base + band + 90;
  });

  return { nodes, edges };
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

  const { nodes, edges } = useMemo(() => build(topo), [topo]);
  const clusterCount = topo?.clusters?.length ?? 0;

  // Reencuadra solo cuando cambia el CONJUNTO de nodos (no en cada poll), para
  // no pelear con el zoom/paneo del usuario.
  const sig = nodes.map((n) => n.id).join("|");
  useEffect(() => {
    const t = setTimeout(() => {
      instance.current?.fitView({ padding: 0.18, duration: 400 });
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
