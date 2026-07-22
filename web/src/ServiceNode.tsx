// Nodo personalizado de React Flow: el "mosaico de servicio" con icono, color
// y estado, en el mismo estilo que el diagrama de arquitectura.
import { Handle, Position } from "reactflow";
import { Icon, type IconKey } from "./icons";
import type { App, AppResource, PodInfo, Usage } from "./api";

// WorkloadOp: datos que necesita el Inspector para operar una carga.
export interface WorkloadOp {
  clusterId: string;
  namespace: string;
  workload: string;
  workloadKind: string;
  replicas: number;
  online: boolean;
}

// Selection: entidad seleccionada en el mapa (clúster o carga) para el Inspector.
export interface Selection {
  key: string; // clave de anotación: "clusterId" o "clusterId/namespace/workload"
  title: string; // nombre real (no editable)
  kind: string; // "Clúster" | "Deployment" | "StatefulSet"
  subtitle: string; // provider o namespace
  op?: WorkloadOp; // presente solo si es operable (carga)
  cluster?: ClusterOps; // presente solo si es un clúster (complementos)
  app?: AppOps; // presente solo si es un proyecto GitOps
  pods?: PodInfo[]; // pods de la carga con sus IPs (para el Inspector)
  usage?: Usage; // CPU/memoria en uso (metrics-server)
  node?: NodeOps; // presente solo si es un NODO del clúster (cordon/drain)
}

// NodeOps: acciones sobre un nodo (acordonar / vaciar / reabrir).
export interface NodeOps {
  clusterId: string;
  name: string;
  online: boolean;
  unschedulable: boolean;
}

// AppOps: acciones sobre un proyecto GitOps (sincronizar / revertir) + su árbol.
export interface AppOps {
  clusterId: string;
  name: string;
  online: boolean;
  sync: string;
  health: string;
  repoURL: string;
  resources: AppResource[]; // árbol de recursos que despliega
}

// ClusterOps: acciones a nivel de clúster (complementos y proyectos GitOps).
export interface ClusterOps {
  clusterId: string;
  online: boolean;
  installedAddons: string[]; // keys de complementos ya instalados (detectados)
  apps: App[]; // proyectos GitOps (Applications de ArgoCD)
}

export interface ServiceNodeData {
  label: string;
  sublabel?: string;
  color: string;
  icon: IconKey;
  online?: boolean;
  muted?: boolean;
  hasNote?: boolean; // muestra un indicador si tiene nota
  op?: WorkloadOp; // presente solo en nodos de carga operables
  sel?: Selection; // entidad editable (clúster o carga)
}

export function ServiceNode({ data }: { data: ServiceNodeData }) {
  return (
    <div className={`svc-node${data.muted ? " is-muted" : ""}`}>
      <Handle type="target" position={Position.Left} />
      <span className="svc-ico" style={{ background: data.color }}>
        <Icon name={data.icon} size={20} />
      </span>
      <div className="svc-meta">
        <span className="svc-label">
          {data.label}
          {data.hasNote && <span className="svc-note" title="tiene nota">✎</span>}
        </span>
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
