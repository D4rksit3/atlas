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
  usage?: Usage | null;
}

export interface Placement {
  node: string;
  pods: number;
}

// Usage: consumo vivo (metrics-server): millicores y MiB en uso.
export interface Usage {
  cpum: number;
  memMi: number;
}

// PodInfo: un pod concreto con su IP real, nodo y fase.
export interface PodInfo {
  name: string;
  ip?: string;
  node?: string;
  phase?: string; // Running | Pending | ...
}

export interface Workload {
  name: string;
  namespace: string;
  kind: string;
  replicas: number;
  placement?: Placement[] | null;
  pods?: PodInfo[] | null;
  usage?: Usage | null;
}

// ServiceInfo: un Service del clúster (el "cable" entre pods) con su ClusterIP,
// puertos y las cargas a las que enruta.
export interface ServicePort {
  port: number;
  protocol?: string;
}
export interface ServiceInfo {
  name: string;
  namespace: string;
  type: string; // ClusterIP | NodePort | LoadBalancer | Headless
  clusterIP?: string;
  ports?: ServicePort[] | null;
  workloads?: string[] | null;
}

export interface Link {
  from: string;
  to: string;
}

export interface AppResource {
  group?: string;
  kind: string;
  namespace?: string;
  name: string;
  status?: string;
  health?: string;
}

export interface App {
  name: string;
  namespace: string;
  repoURL: string;
  path: string;
  revision?: string;
  sync: string; // Synced | OutOfSync | Unknown
  health: string; // Healthy | Progressing | Degraded | ...
  resources?: AppResource[] | null;
}

export interface AddonParam {
  key: string;
  label: string;
  type: string; // string | password | int | bool
  default: string;
  path: string;
}

// AddonAccess: cómo llegar a la UI de un complemento (service + puerto).
export interface AddonAccess {
  service: string;
  port: number;
  hint?: string; // cómo entrar (credenciales iniciales)
}

// AddonInfo: un complemento del catálogo instalable desde la GUI.
export interface AddonInfo {
  key: string;
  name: string;
  category: string; // gitops | monitoreo | seguridad | redes
  description: string;
  namespace: string;
  detectWorkload: string;
  params?: AddonParam[] | null;
  access?: AddonAccess | null;
}

/** Catálogo de complementos instalables. */
export async function fetchAddons(): Promise<AddonInfo[]> {
  const res = await fetch("/v1/addons");
  if (!res.ok) return [];
  return (await res.json()) as AddonInfo[];
}

export interface AppSpec {
  name: string;
  repoURL: string;
  path: string;
  namespace: string;
  revision?: string;
}

export interface IssuerSpec {
  name?: string; // nombre del ClusterIssuer (default letsencrypt-<env>)
  email: string; // cuenta ACME (avisos de expiración)
  environment: "staging" | "production";
  ingressClass?: string; // clase de Ingress para el reto HTTP-01 (default nginx)
}

// IngressInfo: una ruta publicada (host -> service) vista por el agente.
export interface IngressInfo {
  name: string;
  namespace: string;
  class?: string;
  host: string;
  path?: string;
  service: string;
  port: number;
  tls: boolean;
}

export interface Snapshot {
  nodes: Node[] | null;
  workloads: Workload[] | null;
  links: Link[] | null;
  apps?: App[] | null;
  ingresses?: IngressInfo[] | null;
  services?: ServiceInfo[] | null;
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

export type ActionKind =
  | "scale"
  | "restart"
  | "install"
  | "addapp"
  | "sync"
  | "rollback"
  | "issuer"
  | "expose"
  | "uninstall"
  | "unexpose"
  | "logs"
  | "events";
export type ActionStatus = "pending" | "dispatched" | "done" | "error";

// ExposeSpec: publicar un servicio (crear su Ingress host -> service).
export interface ExposeSpec {
  namespace: string;
  service: string;
  port: number;
  host: string;
  ingressClass?: string; // default nginx
  tls?: boolean; // https con cert-manager
  issuer?: string; // ClusterIssuer (default letsencrypt-production)
}

export interface ActionRequest {
  kind: ActionKind;
  namespace?: string;
  workload?: string;
  workloadKind?: string;
  replicas?: number;
  addon?: string; // solo para install
  values?: Record<string, string>; // valores del complemento (solo install)
  app?: AppSpec; // solo para addapp
  issuer?: IssuerSpec; // solo para issuer (crear emisor TLS)
  expose?: ExposeSpec; // solo para expose (publicar un servicio)
}

export interface Action {
  id: string;
  kind: ActionKind;
  namespace: string;
  workload: string;
  workloadKind: string;
  replicas: number;
  addon?: string;
  values?: Record<string, string>; // valores aplicados (passwords enmascaradas)
  output?: string; // salida de logs/events
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
