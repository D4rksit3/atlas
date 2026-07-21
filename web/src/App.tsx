import { useEffect, useState } from "react";
import { TopologyMap } from "./TopologyMap";
import {
  fetchAuthConfig,
  handleCallback,
  currentSession,
  login,
  logout,
  type AuthConfig,
  type Session,
} from "./auth";
import "./styles.css";

export default function App() {
  const [cfg, setCfg] = useState<AuthConfig | null>(null);
  const [sess, setSess] = useState<Session | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      const c = await fetchAuthConfig();
      if (c.enabled) {
        try {
          await handleCallback(c); // por si volvemos del IdP
        } catch (e) {
          setErr(String(e));
        }
      }
      setCfg(c);
      setSess(currentSession());
    })();
  }, []);

  if (!cfg) {
    return <div className="app center muted">cargando…</div>;
  }

  // Auth activa y sin sesión: pantalla de login.
  if (cfg.enabled && !sess) {
    return (
      <div className="app center">
        <div className="login">
          <div className="login-mark">◆</div>
          <div className="login-title">Atlas</div>
          <div className="login-sub">Consola de arquitectura de Kubernetes</div>
          <button className="btn primary login-btn" onClick={() => login(cfg)}>
            Iniciar sesión
          </button>
          {err && <div className="login-err">{err}</div>}
        </div>
      </div>
    );
  }

  return (
    <div className="app">
      <nav className="nav">
        <span className="logo">
          <span className="mark">◆</span> Atlas
        </span>
        <span className="crumb">
          Consola de arquitectura › <b>Topología global</b>
        </span>
        <span className="region">
          {sess ? (
            <span className="user">
              <span className="user-email">{sess.email}</span>
              <button className="user-out" onClick={() => { logout(); setSess(null); }}>
                salir
              </button>
            </span>
          ) : (
            <>
              <span className="dot" /> multi-región · on-prem + nube
            </>
          )}
        </span>
      </nav>
      <main>
        <TopologyMap />
      </main>
    </div>
  );
}
