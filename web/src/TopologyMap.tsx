// El mapa vivo: descarga la topología del control plane cada pocos segundos y
// la dibuja con React Flow. La disposición es sencilla a propósito (columnas);
// más adelante se puede meter un layout automático (dagre/elk) o el editor.
import { useEffect, useMemo, useState } from "react";
import ReactFlow, {
  Background,
  Controls,
  MarkerType,
  type Edge,
  type Node,
} from "reactflow";
import "reactflow/dist/style.css";

import { fetchTopology, type Topology } from "./api";
import { ServiceNode, type ServiceNodeData } from "./ServiceNode";
import { providerColor, providerLabel } from "./icons";

const nodeTypes = { service: ServiceNode };
const POLL_MS = 5000;

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
    position: { x: 0, y: 220 },
    data: { label: "Consola · GUI", color: "#12B5A5", icon: "console" },
  });
  nodes.push({
    id: "control-plane",
    type: "service",
    position: { x: 300, y: 220 },
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
  clusters.forEach((c, i) => {
    const y = i * 200;
    const color = providerColor[c.provider] ?? "#5A6577";
    const clusterId = `cluster-${c.clusterId}`;

    nodes.push({
      id: clusterId,
      type: "service",
      position: { x: 720, y },
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

    // Nodos worker a la derecha del clúster.
    const workers = (c.snapshot?.nodes ?? []).filter((n) => n.role === "worker");
    workers.forEach((n, j) => {
      const nid = `${clusterId}-node-${j}`;
      nodes.push({
        id: nid,
        type: "service",
        position: { x: 980, y: y - 40 + j * 70 },
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
        source: nid,
        target: clusterId,
        style: { stroke: "#0E9E6E", opacity: 0.5 },
      });
    });
  });

  return { nodes, edges };
}

export function TopologyMap() {
  const [topo, setTopo] = useState<Topology | null>(null);
  const [error, setError] = useState<string | null>(null);

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
          fitView
          proOptions={{ hideAttribution: true }}
        >
          <Background gap={28} />
          <Controls />
        </ReactFlow>
      </div>
    </div>
  );
}
