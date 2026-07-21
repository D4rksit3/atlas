// Nodo personalizado de React Flow: el "mosaico de servicio" con icono, color
// y estado, en el mismo estilo que el diagrama de arquitectura.
import { Handle, Position } from "reactflow";
import { Icon, type IconKey } from "./icons";

// WorkloadOp: datos que necesita el Inspector para operar una carga.
export interface WorkloadOp {
  clusterId: string;
  namespace: string;
  workload: string;
  workloadKind: string;
  replicas: number;
  online: boolean;
}

export interface ServiceNodeData {
  label: string;
  sublabel?: string;
  color: string;
  icon: IconKey;
  online?: boolean;
  muted?: boolean;
  op?: WorkloadOp; // presente solo en nodos de carga operables
}

export function ServiceNode({ data }: { data: ServiceNodeData }) {
  return (
    <div className={`svc-node${data.muted ? " is-muted" : ""}`}>
      <Handle type="target" position={Position.Left} />
      <span className="svc-ico" style={{ background: data.color }}>
        <Icon name={data.icon} size={20} />
      </span>
      <div className="svc-meta">
        <span className="svc-label">{data.label}</span>
        {data.sublabel && <span className="svc-sub">{data.sublabel}</span>}
      </div>
      {data.online !== undefined && (
        <span
          className="svc-dot"
          style={{ background: data.online ? "var(--good)" : "var(--faint)" }}
          title={data.online ? "online" : "offline"}
        />
      )}
      <Handle type="source" position={Position.Right} />
    </div>
  );
}
