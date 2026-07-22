// Panel de Usuarios: gestiona el acceso de tu equipo sin compartir LA
// contraseña del admin. Crear/borrar usuarios locales con rol viewer (solo
// lee) u operator (puede operar). Requiere rol operator; todo queda auditado.
import { useEffect, useState, type FormEvent } from "react";
import { authHeaders } from "./api";

interface LocalUser {
  username: string;
  role: string;
  createdBy?: string;
  createdAt: string;
}

export function UsersPanel({ onClose }: { onClose: () => void }) {
  const [users, setUsers] = useState<LocalUser[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [pass, setPass] = useState("");
  const [role, setRole] = useState("viewer");
  const [busy, setBusy] = useState(false);
  const [fb, setFb] = useState<string | null>(null);
  const [confirmDel, setConfirmDel] = useState<string | null>(null);

  const load = async () => {
    try {
      const res = await fetch("/v1/users", { headers: authHeaders() });
      if (res.status === 403) {
        setErr("necesitas rol operator para gestionar usuarios");
        return;
      }
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      setUsers((await res.json()) as LocalUser[]);
      setErr(null);
    } catch (e) {
      setErr(String(e));
    }
  };

  useEffect(() => {
    load();
  }, []);

  const create = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setFb(null);
    try {
      const res = await fetch("/v1/users", {
        method: "POST",
        headers: authHeaders({ "Content-Type": "application/json" }),
        body: JSON.stringify({ username: name.trim(), password: pass, role }),
      });
      const body = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(body.error ?? `HTTP ${res.status}`);
      setFb(`usuario ${name.trim()} creado ✓ — pásale su contraseña por un canal seguro`);
      setName("");
      setPass("");
      load();
    } catch (ex) {
      setFb(`error: ${ex instanceof Error ? ex.message : String(ex)}`);
    } finally {
      setBusy(false);
    }
  };

  const remove = async (username: string) => {
    setConfirmDel(null);
    try {
      const res = await fetch(`/v1/users/${encodeURIComponent(username)}`, {
        method: "DELETE",
        headers: authHeaders(),
      });
      const body = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(body.error ?? `HTTP ${res.status}`);
      setFb(`usuario ${username} eliminado`);
      load();
    } catch (ex) {
      setFb(`error: ${ex instanceof Error ? ex.message : String(ex)}`);
    }
  };

  return (
    <aside className="audit users-panel">
      <div className="audit-head">
        <div className="audit-title">Usuarios</div>
        <button className="insp-x" onClick={onClose} aria-label="cerrar">
          ×
        </button>
      </div>
      <div className="audit-body">
        {err && <div className="audit-empty err">{err}</div>}
        {!err && (
          <>
            <div className="users-row users-row-head">
              <span className="users-name">admin</span>
              <span className="users-role">operator</span>
              <span className="users-note">del instalador</span>
            </div>
            {users?.map((u) => (
              <div className="users-row" key={u.username}>
                <span className="users-name">{u.username}</span>
                <span className={`users-role${u.role === "operator" ? " op" : ""}`}>{u.role}</span>
                {confirmDel === u.username ? (
                  <span className="users-del-confirm">
                    <button className="btn danger" onClick={() => remove(u.username)}>borrar</button>
                    <button className="btn" onClick={() => setConfirmDel(null)}>no</button>
                  </span>
                ) : (
                  <button className="users-del" onClick={() => setConfirmDel(u.username)} title="eliminar">
                    ×
                  </button>
                )}
              </div>
            ))}
            {users && users.length === 0 && (
              <div className="audit-empty">solo existe el admin — crea usuarios para tu equipo</div>
            )}

            <div className="insp-section">Nuevo usuario</div>
            <form className="svc-form users-form" onSubmit={create}>
              <label>
                usuario
                <input value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" />
              </label>
              <label>
                contraseña (≥8)
                <input type="password" value={pass} onChange={(e) => setPass(e.target.value)} autoComplete="new-password" />
              </label>
              <label>
                rol
                <select value={role} onChange={(e) => setRole(e.target.value)}>
                  <option value="viewer">viewer — solo lee el mapa</option>
                  <option value="operator">operator — puede operar</option>
                </select>
              </label>
              <button className="btn primary" type="submit" disabled={busy || name.trim().length < 2 || pass.length < 8}>
                {busy ? "creando…" : "Crear usuario"}
              </button>
            </form>
            {fb && <div className="svc-notice">{fb}</div>}
          </>
        )}
      </div>
    </aside>
  );
}
