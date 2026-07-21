// Layout automático del mapa con dagre (grafo dirigido por capas).
//
// Separamos el grafo de DISPOSICIÓN del grafo VISUAL: dagre ordena usando una
// jerarquía limpia (consola → control plane → clúster → nodos → cargas), mientras
// las aristas que se dibujan conservan su dirección semántica (el agente marca
// hacia casa, las conexiones de Hubble apuntan origen→destino). Así el orden es
// legible sin falsear las flechas.
import Dagre from "@dagrejs/dagre";
import type { Node } from "reactflow";

export interface LayoutEdge {
  source: string;
  target: string;
}

export interface NodeSize {
  width: number;
  height: number;
}

/** Estima el ancho de un mosaico según su etiqueta (icono + texto + estado). */
export function sizeFor(label: string, sublabel?: string): NodeSize {
  const longest = Math.max(label.length, (sublabel?.length ?? 0) + 2);
  const width = Math.min(300, Math.max(168, 96 + longest * 8.2));
  return { width, height: 60 };
}

/**
 * Posiciona los nodos con dagre. `layoutEdges` define la jerarquía (no se dibuja);
 * `sizes` da el tamaño de cada mosaico para que no se solapen.
 */
export function layout<T>(
  nodes: Node<T>[],
  layoutEdges: LayoutEdge[],
  sizes: Map<string, NodeSize>,
  dir: "LR" | "TB" = "LR",
): Node<T>[] {
  const g = new Dagre.graphlib.Graph().setDefaultEdgeLabel(() => ({}));
  g.setGraph({ rankdir: dir, nodesep: 24, ranksep: 104, marginx: 28, marginy: 28 });

  nodes.forEach((n) => {
    const s = sizes.get(n.id) ?? { width: 210, height: 60 };
    g.setNode(n.id, { width: s.width, height: s.height });
  });
  layoutEdges.forEach((e) => {
    if (g.hasNode(e.source) && g.hasNode(e.target)) g.setEdge(e.source, e.target);
  });

  Dagre.layout(g);

  return nodes.map((n) => {
    const p = g.node(n.id);
    const s = sizes.get(n.id) ?? { width: 210, height: 60 };
    // dagre da el centro; React Flow quiere la esquina superior izquierda.
    return { ...n, position: { x: p.x - s.width / 2, y: p.y - s.height / 2 } };
  });
}
