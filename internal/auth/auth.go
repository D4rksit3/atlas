// Package auth implementa la autenticación (OIDC) y autorización (RBAC) de la
// GUI. El control plane verifica un JWT de OIDC (firma vía JWKS del IdP, iss,
// aud, exp) y asigna un rol: cualquier usuario autenticado es 'viewer' (lee); los
// de la lista de operadores son 'operator' (pueden ejecutar acciones).
//
// Solo protege los endpoints de la GUI. Los del agente usan mTLS, no OIDC.
package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Roles del sistema (orden de privilegio: viewer < operator).
const (
	RoleViewer   = "viewer"
	RoleOperator = "operator"
)

// Config parametriza la autenticación.
type Config struct {
	Issuer    string   // URL del IdP OIDC (p. ej. https://accounts.google.com)
	ClientID  string   // audiencia esperada del token (client id de la GUI)
	Operators []string // emails o grupos que obtienen rol 'operator'
	Scopes    []string // scopes que la GUI pedirá (openid, email, profile...)
}

// Authenticator verifica tokens y decide roles.
type Authenticator struct {
	verifier  *oidc.IDTokenVerifier
	operators map[string]bool
	cfg       Config
}

// New crea el autenticador descubriendo el IdP (lee su configuración OIDC).
func New(ctx context.Context, cfg Config) (*Authenticator, error) {
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("descubriendo el IdP OIDC %q: %w", cfg.Issuer, err)
	}
	ops := make(map[string]bool, len(cfg.Operators))
	for _, o := range cfg.Operators {
		if o = strings.TrimSpace(o); o != "" {
			ops[o] = true
		}
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	}
	return &Authenticator{
		verifier:  provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		operators: ops,
		cfg:       cfg,
	}, nil
}

// PublicConfig es lo que la GUI necesita para iniciar el login (nada secreto).
type PublicConfig struct {
	Enabled  bool     `json:"enabled"`
	Issuer   string   `json:"issuer"`
	ClientID string   `json:"clientId"`
	Scopes   []string `json:"scopes"`
}

// PublicConfig devuelve la config pública para la GUI.
func (a *Authenticator) PublicConfig() PublicConfig {
	return PublicConfig{Enabled: true, Issuer: a.cfg.Issuer, ClientID: a.cfg.ClientID, Scopes: a.cfg.Scopes}
}

type claims struct {
	Email  string   `json:"email"`
	Groups []string `json:"groups"`
	Sub    string   `json:"sub"`
}

// User es la identidad autenticada que se adjunta a la petición.
type User struct {
	Subject string
	Email   string
	Role    string
}

func (a *Authenticator) roleFor(c claims) string {
	if a.operators[c.Email] {
		return RoleOperator
	}
	for _, g := range c.Groups {
		if a.operators[g] {
			return RoleOperator
		}
	}
	return RoleViewer
}

type ctxKey struct{}

// Require devuelve un middleware que exige un token válido y, si minRole es
// operator, que el usuario sea operador. Adjunta el User al contexto.
func (a *Authenticator) Require(minRole string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := bearer(r)
		if raw == "" {
			unauthorized(w, "falta el token de sesión (inicia sesión)")
			return
		}
		idToken, err := a.verifier.Verify(r.Context(), raw)
		if err != nil {
			unauthorized(w, "token inválido o expirado")
			return
		}
		var c claims
		if err := idToken.Claims(&c); err != nil {
			unauthorized(w, "no se pudieron leer los claims del token")
			return
		}
		user := User{Subject: c.Sub, Email: c.Email, Role: a.roleFor(c)}
		if minRole == RoleOperator && user.Role != RoleOperator {
			forbidden(w, "tu usuario no tiene permiso para operar (rol: "+user.Role+")")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKey{}, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserFrom recupera el usuario autenticado del contexto (si lo hay).
func UserFrom(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(ctxKey{}).(User)
	return u, ok
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[len("Bearer "):])
	}
	return ""
}

func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}

func forbidden(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}
