// Cliente OIDC de la GUI: Authorization Code + PKCE (cliente público, sin
// secreto). Obtiene un id_token del IdP y lo envía como Bearer a la API. El
// control plane lo verifica (firma/iss/aud/exp) y aplica RBAC.

export interface AuthConfig {
  enabled: boolean;
  /** Métodos disponibles: "local" (usuario/contraseña de Atlas) y/o "oidc". */
  methods?: string[];
  issuer?: string;
  clientId?: string;
  scopes?: string[];
}

export interface Session {
  token: string;
  email: string;
  exp: number; // epoch segundos
}

const TOKEN_KEY = "atlas.token";
const SESSION_KEY = "atlas.session"; // sesión del login local (JSON, no es un JWT)
let session: Session | null = loadSession();

/** Token actual (o null si no hay o expiró). Lo usa el cliente de API. */
export function getToken(): string | null {
  if (session && session.exp * 1000 > Date.now()) return session.token;
  return null;
}

export function currentSession(): Session | null {
  return getToken() ? session : null;
}

export async function fetchAuthConfig(): Promise<AuthConfig> {
  const res = await fetch("/v1/authconfig");
  if (!res.ok) return { enabled: false };
  return (await res.json()) as AuthConfig;
}

/** Login local integrado: usuario/contraseña contra POST /v1/login. */
export async function loginLocal(username: string, password: string): Promise<Session> {
  const res = await fetch("/v1/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password }),
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error ?? `login falló (HTTP ${res.status})`);
  }
  const data = (await res.json()) as { token: string; user: string; exp: number };
  session = { token: data.token, email: data.user, exp: data.exp };
  localStorage.setItem(SESSION_KEY, JSON.stringify(session));
  return session;
}

/** Inicia el login: PKCE + redirección al IdP. */
export async function login(cfg: AuthConfig): Promise<void> {
  const meta = await discover(cfg.issuer!);
  const verifier = randomString(64);
  const challenge = await s256(verifier);
  const state = randomString(24);
  sessionStorage.setItem("atlas.pkce", verifier);
  sessionStorage.setItem("atlas.state", state);

  const p = new URLSearchParams({
    response_type: "code",
    client_id: cfg.clientId!,
    redirect_uri: redirectUri(),
    scope: (cfg.scopes ?? ["openid", "email", "profile"]).join(" "),
    state,
    code_challenge: challenge,
    code_challenge_method: "S256",
  });
  window.location.assign(`${meta.authorization_endpoint}?${p.toString()}`);
}

/** Si la URL trae ?code&state (vuelta del IdP), canjea el code por el token. */
export async function handleCallback(cfg: AuthConfig): Promise<boolean> {
  const url = new URL(window.location.href);
  const code = url.searchParams.get("code");
  const state = url.searchParams.get("state");
  if (!code) return false;
  if (state !== sessionStorage.getItem("atlas.state")) {
    throw new Error("state OIDC no coincide (posible CSRF)");
  }
  const verifier = sessionStorage.getItem("atlas.pkce") ?? "";
  const meta = await discover(cfg.issuer!);
  const res = await fetch(meta.token_endpoint, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "authorization_code",
      code,
      redirect_uri: redirectUri(),
      client_id: cfg.clientId!,
      code_verifier: verifier,
    }),
  });
  if (!res.ok) throw new Error(`intercambio de token falló: HTTP ${res.status}`);
  const data = await res.json();
  const token: string = data.id_token ?? data.access_token;
  session = sessionFromToken(token);
  localStorage.setItem(TOKEN_KEY, token);
  sessionStorage.removeItem("atlas.pkce");
  sessionStorage.removeItem("atlas.state");
  // Limpia el ?code de la URL.
  window.history.replaceState({}, "", url.pathname);
  return true;
}

export function logout(): void {
  session = null;
  localStorage.removeItem(TOKEN_KEY);
  localStorage.removeItem(SESSION_KEY);
}

// ---- helpers ----

interface Meta {
  authorization_endpoint: string;
  token_endpoint: string;
}
async function discover(issuer: string): Promise<Meta> {
  const res = await fetch(`${issuer.replace(/\/$/, "")}/.well-known/openid-configuration`);
  if (!res.ok) throw new Error(`no pude descubrir el IdP (HTTP ${res.status})`);
  return (await res.json()) as Meta;
}

function redirectUri(): string {
  return window.location.origin + window.location.pathname;
}

function loadSession(): Session | null {
  // Sesión del login local (JSON con token+usuario+caducidad).
  const s = localStorage.getItem(SESSION_KEY);
  if (s) {
    try {
      return JSON.parse(s) as Session;
    } catch {
      localStorage.removeItem(SESSION_KEY);
    }
  }
  // Sesión OIDC (JWT del IdP).
  const t = localStorage.getItem(TOKEN_KEY);
  if (!t) return null;
  try {
    return sessionFromToken(t);
  } catch {
    return null;
  }
}

function sessionFromToken(token: string): Session {
  const claims = JSON.parse(atob(token.split(".")[1].replace(/-/g, "+").replace(/_/g, "/")));
  return { token, email: claims.email ?? claims.sub ?? "usuario", exp: claims.exp ?? 0 };
}

function randomString(len: number): string {
  const bytes = new Uint8Array(len);
  crypto.getRandomValues(bytes);
  return base64url(bytes).slice(0, len);
}

async function s256(input: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(input));
  return base64url(new Uint8Array(digest));
}

function base64url(bytes: Uint8Array): string {
  let s = "";
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}
