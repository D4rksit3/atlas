// Nodo-grupo: una "caja" que representa un nodo (servidor) del clúster con las
// cargas cuyos pods corren EN él. Es la vista "agrupar por nodo": hace visible la
// pertenencia pod→nodo (un servidor = un nodo, con sus pods dentro). Al clicar una
// carga se abre el Inspector, igual que en la vista de flujo.
import { Handle, Position } from "reactflow";
import { Icon, type IconKey } from "./icons";
import type { Selection } from "./ServiceNode";

export interface NodeGroupItem {
  key: string;
  label: string;
  color: string;
  icon: IconKey;
  pods: number;
  muted: boolean;
  sel: Selection;
}

export interface NodeGroupData {
  nodeName: string;
  role: string; // control-plane | worker
  online: boolean;
  color: string;
  items: NodeGroupItem[];
  onSelect: (sel: Selection) => void;
  usage?: string; // consumo vivo "123m · 456Mi" (metrics-server)
}

export function NodeGroup({ data }: { data: NodeGroupData }) {
  return (
    <div className={`nodegroup${data.online ? "" : " is-off"}`}>
      <Handle type="target" position={Position.Left} />
      <div className="ng-head">
        <span className="ng-ico" style={{ background: data.color }}>
          <Icon name="server" size={16} />
        </span>
        <span className="ng-name">{data.nodeName}</span>
        <span className="ng-role">{data.role}{data.usage ? ` · ${data.usage}` : ""}</span>
        <span
          className="ng-dot"
          style={{ background: data.online ? "var(--good)" : "var(--faint)" }}
        />
      </div>
      <div className="ng-items">
        {data.items.length === 0 && <div className="ng-empty">sin cargas</div>}
        {data.items.map((it) => (
          <button
            key={it.key}
            className={`ng-item${it.muted ? " muted" : ""}`}
            onClick={(e) => {
              e.stopPropagation();
              data.onSelect(it.sel);
            }}
            title={it.sel.subtitle + " / " + it.label}
          >
            <span className="ng-item-ico" style={{ background: it.color }}>
              <Icon name={it.icon} size={12} />
            </span>
            <span className="ng-item-label">{it.label}</span>
            <span className="ng-item-pods">×{it.pods}</span>
          </button>
        ))}
      </div>
    </div>
  );
}
