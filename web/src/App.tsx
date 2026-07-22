import { useEffect, useState, type FormEvent } from "react";
import { TopologyMap } from "./TopologyMap";
import { Onboarding } from "./Onboarding";
import {
  fetchAuthConfig,
  handleCallback,
  currentSession,
  login,
  loginLocal,
  logout,
  type AuthConfig,
  type Session,
} from "./auth";
import "./styles.css";

export default function App() {
  const [cfg, setCfg] = useState<AuthConfig | null>(null);
  const [sess, setSess] = useState<Session | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [onboarding, setOnboarding] = useState(false);

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
    return <LoginScreen cfg={cfg} err={err} onSession={setSess} />;
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
        <button className="link-cluster" onClick={() => setOnboarding(true)}>
          + Vincular clúster
        </button>
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
      {onboarding && <Onboarding onClose={() => setOnboarding(false)} />}
    </div>
  );
}

/** Pantalla de login: formulario local (usuario/contraseña) y/o botón SSO
 *  (OIDC), según los métodos que anuncie el control plane. */
function LoginScreen({
  cfg,
  err,
  onSession,
}: {
  cfg: AuthConfig;
  err: string | null;
  onSession: (s: Session) => void;
}) {
  const [user, setUser] = useState("");
  const [pass, setPass] = useState("");
  const [busy, setBusy] = useState(false);
  const [localErr, setLocalErr] = useState<string | null>(null);
  const methods = cfg.methods ?? ["oidc"]; // control planes antiguos: solo OIDC
  const hasLocal = methods.includes("local");
  const hasOIDC = methods.includes("oidc");

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setLocalErr(null);
    try {
      onSession(await loginLocal(user, pass));
    } catch (ex) {
      setLocalErr(ex instanceof Error ? ex.message : String(ex));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="app center">
      <div className="login">
        <div className="login-mark">◆</div>
        <div className="login-title">Atlas</div>
        <div className="login-sub">Consola de arquitectura de Kubernetes</div>
        {hasLocal && (
          <form className="login-form" onSubmit={submit}>
            <input
              className="login-input"
              placeholder="usuario"
              autoComplete="username"
              value={user}
              onChange={(e) => setUser(e.target.value)}
              autoFocus
            />
            <input
              className="login-input"
              type="password"
              placeholder="contraseña"
              autoComplete="current-password"
              value={pass}
              onChange={(e) => setPass(e.target.value)}
            />
            <button className="btn primary login-btn" type="submit" disabled={busy || !user || !pass}>
              {busy ? "entrando…" : "Iniciar sesión"}
            </button>
          </form>
        )}
        {hasLocal && hasOIDC && <div className="login-or">o</div>}
        {hasOIDC && (
          <button className="btn login-btn" onClick={() => login(cfg)}>
            Entrar con SSO
          </button>
        )}
        {(localErr || err) && <div className="login-err">{localErr ?? err}</div>}
      </div>
    </div>
  );
}
