// Tipos espejo de pkg/api/types.go y el cliente de la API del control plane.
// Si cambias el contrato en Go, actualiza estos tipos.
import { getToken } from "./auth";

export type Provider = "onprem" | "aws" | "oci";

/** authHeaders añade el Bearer OIDC si hay sesión (si no, va sin auth: dev). */
function authHeaders(extra?: Record<string, string>): Record<string, string> {
  const h: Record<string, string> = { ...(extra ?? {}) };
  const t = getToken();
  if (t) h["Authorization"] = `Bearer ${t}`;
  return h;
}

export interface Node {
  name: string;
  role: "control-plane" | "worker" | string;
  ready: boolean;
}

export interface Placement {
  node: string;
  pods: number;
}

export interface Workload {
  name: string;
  namespace: string;
  kind: string;
  replicas: number;
  placement?: Placement[] | null;
}

export interface Link {
  from: string;
  to: string;
}

export interface Snapshot {
  nodes: Node[] | null;
  workloads: Workload[] | null;
  links: Link[] | null;
}

export interface ClusterView {
  clusterId: string;
  name: string;
  provider: Provider;
  online: boolean;
  lastSeen: string;
  agentVersion: string;
  snapshot: Snapshot;
}

export interface Topology {
  clusters: ClusterView[] | null;
  generatedAt: string;
}

/** Descarga la topología agregada desde el control plane. */
export async function fetchTopology(): Promise<Topology> {
  const res = await fetch("/v1/topology", { headers: authHeaders() });
  if (!res.ok) throw new Error(`topology HTTP ${res.status}`);
  return (await res.json()) as Topology;
}

// ---- Acciones: la GUI ordena, el agente ejecuta ----

export type ActionKind = "scale" | "restart" | "install";
export type ActionStatus = "pending" | "dispatched" | "done" | "error";

export interface ActionRequest {
  kind: ActionKind;
  namespace?: string;
  workload?: string;
  workloadKind?: string;
  replicas?: number;
  addon?: string; // solo para install
}

export interface Action {
  id: string;
  kind: ActionKind;
  namespace: string;
  workload: string;
  workloadKind: string;
  replicas: number;
  status: ActionStatus;
  error?: string;
  createdAt: string;
  updatedAt: string;
}

/** Encola una acción sobre una carga de un clúster. */
export async function postAction(
  clusterId: string,
  req: ActionRequest,
): Promise<Action> {
  const res = await fetch(`/v1/clusters/${encodeURIComponent(clusterId)}/actions`, {
    method: "POST",
    headers: authHeaders({ "Content-Type": "application/json" }),
    body: JSON.stringify(req),
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error ?? `HTTP ${res.status}`);
  }
  return (await res.json()) as Action;
}

// ---- Anotaciones: metadatos editables del mapa ----

export interface Annotation {
  displayName?: string;
  color?: string;
  note?: string;
}

/** Lee todas las anotaciones (metadatos) para superponerlas al mapa. */
export async function fetchAnnotations(): Promise<Record<string, Annotation>> {
  const res = await fetch("/v1/annotations", { headers: authHeaders() });
  if (!res.ok) throw new Error(`annotations HTTP ${res.status}`);
  return (await res.json()) as Record<string, Annotation>;
}

/** Guarda (o borra, si va vacía) la anotación de una entidad del mapa. */
export async function putAnnotation(key: string, a: Annotation): Promise<void> {
  const res = await fetch(`/v1/annotations/${key}`, {
    method: "PUT",
    headers: authHeaders({ "Content-Type": "application/json" }),
    body: JSON.stringify(a),
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error ?? `HTTP ${res.status}`);
  }
}

// ---- Auditoría ----

export interface AuditEntry {
  id: string;
  time: string;
  actor: string;
  event: "action.requested" | "action.executed" | string;
  cluster: string;
  namespace: string;
  workload: string;
  summary: string;
  outcome: "pending" | "ok" | "error" | string;
  error?: string;
}

/** Lee el registro de auditoría (más recientes primero). */
export async function fetchAudit(): Promise<AuditEntry[]> {
  const res = await fetch("/v1/audit", { headers: authHeaders() });
  if (!res.ok) throw new Error(`audit HTTP ${res.status}`);
  return (await res.json()) as AuditEntry[];
}

/** Lista las acciones recientes de un clúster (para ver su estado). */
export async function fetchActions(clusterId: string): Promise<Action[]> {
  const res = await fetch(`/v1/clusters/${encodeURIComponent(clusterId)}/actions`, {
    headers: authHeaders(),
  });
  if (!res.ok) throw new Error(`actions HTTP ${res.status}`);
  return (await res.json()) as Action[];
}
