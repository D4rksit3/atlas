// Asistente "Vincular clúster": genera los comandos y el manifiesto listos para
// copiar y así conectar OTRO clúster/servidor a este control plane. No expone la
// CA ni muta nada — solo genera artefactos que tú ejecutas (cert offline +
// manifiesto del agente con mTLS).
import { useState } from "react";

function slug(s: string): string {
  return s
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "") || "mi-cluster";
}

function Block({ title, code }: { title: string; code: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="ob-block">
      <div className="ob-block-head">
        <span>{title}</span>
        <button
          className="ob-copy"
          onClick={() => {
            navigator.clipboard?.writeText(code);
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          }}
        >
          {copied ? "copiado ✓" : "copiar"}
        </button>
      </div>
      <pre className="ob-code">{code}</pre>
    </div>
  );
}

export function Onboarding({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState("");
  const [provider, setProvider] = useState("onprem");
  const [cp, setCp] = useState(window.location.origin);

  const id = slug(name || "mi cluster");
  const cpURL = cp.replace(/\/$/, "");

  const certCmd = `# 1) Genera el certificado del agente (usa tu CA de Atlas, offline)
go run ./cmd/atlas-certs client --out certs --name ${id}`;

  const secretCmd = `# 2) Crea el Secret con el certificado en el clúster nuevo
kubectl create namespace atlas-system 2>/dev/null || true
kubectl -n atlas-system create secret generic atlas-agent-tls \\
  --from-file=tls.crt=certs/${id}.crt \\
  --from-file=tls.key=certs/${id}.key \\
  --from-file=ca.crt=certs/ca.crt`;

  const manifest = `# 3) Despliega el agente (marca hacia casa por mTLS)
cat <<'EOF' | kubectl apply -f -
apiVersion: v1
kind: ServiceAccount
metadata: { name: atlas-agent, namespace: atlas-system }
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: { name: atlas-agent-readonly-${id} }
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: atlas-agent-readonly }
subjects: [{ kind: ServiceAccount, name: atlas-agent, namespace: atlas-system }]
---
apiVersion: apps/v1
kind: Deployment
metadata: { name: atlas-agent, namespace: atlas-system, labels: { app: atlas-agent } }
spec:
  replicas: 1
  selector: { matchLabels: { app: atlas-agent } }
  template:
    metadata: { labels: { app: atlas-agent } }
    spec:
      serviceAccountName: atlas-agent
      securityContext: { runAsNonRoot: true, runAsUser: 65532 }
      containers:
        - name: agent
          image: ghcr.io/atlasctl/atlas-agent:latest
          args:
            - "--collector=kube"
            - "--transport=grpc"
            - "--name=${name || "mi clúster"}"
            - "--provider=${provider}"
            - "--cluster-id=${id}"
            - "--control-plane=${cpURL}"
            - "--tls-cert=/tls/tls.crt"
            - "--tls-key=/tls/tls.key"
            - "--tls-ca=/tls/ca.crt"
          volumeMounts: [{ name: tls, mountPath: /tls, readOnly: true }]
          securityContext: { allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: { drop: ["ALL"] } }
      volumes:
        - { name: tls, secret: { secretName: atlas-agent-tls } }
EOF`;

  return (
    <div className="ob-backdrop" onClick={onClose}>
      <div className="ob-modal" onClick={(e) => e.stopPropagation()}>
        <div className="ob-head">
          <div>
            <div className="ob-title">Vincular un clúster</div>
            <div className="ob-sub">Genera el certificado y el manifiesto del agente. No expone la CA.</div>
          </div>
          <button className="insp-x" onClick={onClose} aria-label="cerrar">×</button>
        </div>

        <div className="ob-form">
          <div className="ob-field">
            <label className="insp-label">Nombre del clúster</label>
            <input className="insp-input" placeholder="prod eks" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div className="ob-field">
            <label className="insp-label">Proveedor</label>
            <select className="insp-input" value={provider} onChange={(e) => setProvider(e.target.value)}>
              <option value="onprem">On-premises</option>
              <option value="aws">AWS</option>
              <option value="oci">OCI</option>
            </select>
          </div>
          <div className="ob-field ob-wide">
            <label className="insp-label">URL del control plane (accesible por el agente)</label>
            <input className="insp-input" value={cp} onChange={(e) => setCp(e.target.value)} />
          </div>
        </div>

        <div className="ob-body">
          <Block title="1 · Certificado del agente" code={certCmd} />
          <Block title="2 · Secret con el certificado" code={secretCmd} />
          <Block title="3 · Desplegar el agente" code={manifest} />
          <div className="ob-note">
            El agente aparecerá en el mapa en cuanto marque hacia casa. Ajusta la imagen si publicas
            la tuya (<span className="mono">ghcr.io/TU-ORG/atlas-agent</span>).
          </div>
        </div>
      </div>
    </div>
  );
}
