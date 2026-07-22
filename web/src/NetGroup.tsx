// Caja de NAMESPACE para la vista "Red": ordena la comunicación interna del
// clúster sin marañas de aristas. Dentro de cada caja se lee la cadena completa:
//   🌐 host publicado  →  ⇄ Service (ClusterIP:puerto)  →  ▢ cargas (pods+IPs)
// Clicar una carga abre el Inspector con las IPs de sus pods.
import { Handle, Position } from "reactflow";
import { Icon } from "./icons";
import type { Selection } from "./ServiceNode";

/** Una carga dentro de un service (o suelta): clicable hacia el Inspector. */
export interface NetWorkloadRef {
  key: string;
  label: string;
  kind: string; // Deployment | StatefulSet | DaemonSet
  replicas: number;
  ips: string[]; // IPs de sus pods (recortadas para mostrar)
  sel: Selection;
}

/** Un Service con su ClusterIP/puertos, hosts publicados y cargas destino. */
export interface NetServiceRow {
  key: string;
  name: string;
  addr: string; // "10.43.10.20:80" | "headless" | "sin ClusterIP"
  hosts: string[]; // hosts de Ingress que le apuntan
  workloads: NetWorkloadRef[];
}

export interface NetGroupData {
  namespace: string;
  muted: boolean; // namespace de sistema
  services: NetServiceRow[];
  orphans: NetWorkloadRef[]; // cargas sin Service (no reciben tráfico interno)
  onSelect: (sel: Selection) => void;
}

/** Altura de la caja para el layout de dagre. Debe casar con el CSS real: si se
 *  queda corta, las cajas se SOLAPAN en el mapa. Medidas: cabecera 45, padding
 *  del cuerpo 14, cada bloque de service = 10 (borde+padding) + hosts×22 +
 *  línea 24 + cargas×21, separación entre bloques 6, y un margen de seguridad. */
export function netGroupHeight(d: NetGroupData): number {
  const blocks: number[] = d.services.map(
    (s) => 10 + s.hosts.length * 22 + 24 + s.workloads.length * 21,
  );
  if (d.orphans.length) blocks.push(10 + 24 + d.orphans.length * 21);
  const body = blocks.length
    ? blocks.reduce((a, b) => a + b, 0) + (blocks.length - 1) * 6
    : 30; // "sin cargas"
  return 45 + 14 + body + 10;
}

function WorkloadRow({ w, onSelect }: { w: NetWorkloadRef; onSelect: (s: Selection) => void }) {
  return (
    <button
      className="net-wl"
      onClick={(e) => {
        e.stopPropagation();
        onSelect(w.sel);
      }}
      title={`${w.kind} · ${w.replicas} réplica(s) — clic para ver sus pods e IPs`}
    >
      <span className="net-wl-ico">
        <Icon name={w.kind === "StatefulSet" ? "database" : "workload"} size={11} />
      </span>
      <span className="net-wl-label">{w.label}</span>
      <span className="net-wl-ips">
        {w.ips.length > 0 ? w.ips.join(" · ") : `×${w.replicas}`}
      </span>
    </button>
  );
}

export function NetGroup({ data }: { data: NetGroupData }) {
  return (
    <div className={`netgroup${data.muted ? " is-muted" : ""}`}>
      <Handle type="target" position={Position.Left} />
      <div className="net-head">
        <span className="net-ns-ico">
          <Icon name="cluster" size={14} />
        </span>
        <span className="net-ns-name">{data.namespace}</span>
        <span className="net-ns-count">
          {data.services.length} svc · {data.services.reduce((n, s) => n + s.workloads.length, 0) + data.orphans.length} cargas
        </span>
      </div>
      <div className="net-body">
        {data.services.map((s) => (
          <div className="net-svc" key={s.key}>
            {s.hosts.map((h) => (
              <div className="net-host" key={h} title="publicado por Ingress">
                <span className="net-host-ico">🌐</span> {h}
              </div>
            ))}
            <div className="net-svc-line" title={`Service ${s.name} — los pods se hablan a través de él`}>
              <span className="net-svc-ico">⇄</span>
              <span className="net-svc-name">{s.name}</span>
              <span className="net-svc-addr">{s.addr}</span>
            </div>
            {s.workloads.map((w) => (
              <WorkloadRow key={w.key} w={w} onSelect={data.onSelect} />
            ))}
          </div>
        ))}
        {data.orphans.length > 0 && (
          <div className="net-svc">
            <div className="net-svc-line is-orphan" title="cargas sin Service delante">
              <span className="net-svc-ico">◌</span>
              <span className="net-svc-name">sin service</span>
            </div>
            {data.orphans.map((w) => (
              <WorkloadRow key={w.key} w={w} onSelect={data.onSelect} />
            ))}
          </div>
        )}
        {data.services.length === 0 && data.orphans.length === 0 && (
          <div className="net-empty">sin cargas</div>
        )}
      </div>
    </div>
  );
}
