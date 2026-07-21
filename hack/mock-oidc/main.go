// IdP OIDC de PRUEBA (solo para tests E2E de Atlas). Firma RS256 con una clave
// generada al vuelo y sirve: discovery, JWKS, authorize+token (PKCE) y un helper
// /mint para acuñar tokens directamente. Stdlib only.
//
//	go run mock_oidc.go -addr :9000 -client atlas-gui
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	key      *rsa.PrivateKey
	issuer   string
	client   string
	defEmail string
	defGroup string
	kid      = "atlas-test-key"
	codes    = map[string]codeData{} // code -> datos
)

type codeData struct {
	challenge string
	email     string
	groups    []string
	nonce     string
}

func main() {
	addr := flag.String("addr", ":9000", "")
	flag.StringVar(&client, "client", "atlas-gui", "")
	flag.StringVar(&defEmail, "email", "user@atlas.dev", "email que inicia sesión por /authorize")
	flag.StringVar(&defGroup, "groups", "", "grupos (coma-separados) del usuario de /authorize")
	iss := flag.String("issuer", "", "issuer público (por defecto http://localhost<addr>)")
	flag.Parse()

	var err error
	key, err = rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatal(err)
	}
	issuer = *iss
	if issuer == "" {
		issuer = "http://localhost" + *addr
	}

	http.HandleFunc("/.well-known/openid-configuration", discovery)
	http.HandleFunc("/jwks", jwks)
	http.HandleFunc("/authorize", authorize)
	http.HandleFunc("/token", token)
	http.HandleFunc("/mint", mint) // helper de test: /mint?email=&groups=
	log.Printf("mock-oidc en %s (issuer=%s, client=%s)", *addr, issuer, client)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/authorize",
		"token_endpoint":                        issuer + "/token",
		"jwks_uri":                              issuer + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func jwks(w http.ResponseWriter, _ *http.Request) {
	n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
	writeJSON(w, map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid, "n": n, "e": e,
	}}})
}

// authorize acepta la petición PKCE y redirige con un code (auto-aprueba). El
// usuario se elige por login_hint (email) y el query 'groups'.
func authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect := q.Get("redirect_uri")
	state := q.Get("state")
	email := q.Get("login_hint")
	if email == "" {
		email = defEmail
	}
	var groups []string
	if g := q.Get("groups"); g != "" {
		groups = strings.Split(g, ",")
	} else if defGroup != "" {
		groups = strings.Split(defGroup, ",")
	}
	code := randStr()
	codes[code] = codeData{challenge: q.Get("code_challenge"), email: email, groups: groups, nonce: q.Get("nonce")}
	u, _ := url.Parse(redirect)
	pq := u.Query()
	pq.Set("code", code)
	pq.Set("state", state)
	u.RawQuery = pq.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	code := r.Form.Get("code")
	verifier := r.Form.Get("code_verifier")
	cd, ok := codes[code]
	if !ok {
		http.Error(w, "invalid_grant", 400)
		return
	}
	delete(codes, code)
	// Verifica PKCE S256.
	if cd.challenge != "" {
		sum := sha256.Sum256([]byte(verifier))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != cd.challenge {
			http.Error(w, "invalid PKCE", 400)
			return
		}
	}
	idt := signIDToken(cd.email, cd.groups, cd.nonce)
	writeJSON(w, map[string]any{
		"access_token": idt, "id_token": idt, "token_type": "Bearer", "expires_in": 3600,
	})
}

// mint: helper de test para obtener un id_token sin el baile del navegador.
func mint(w http.ResponseWriter, r *http.Request) {
	email := r.URL.Query().Get("email")
	if email == "" {
		email = "user@atlas.dev"
	}
	var groups []string
	if g := r.URL.Query().Get("groups"); g != "" {
		groups = strings.Split(g, ",")
	}
	fmt.Fprint(w, signIDToken(email, groups, ""))
}

func signIDToken(email string, groups []string, nonce string) string {
	now := time.Now()
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid}
	payload := map[string]any{
		"iss": issuer, "aud": client, "sub": "test|" + email, "email": email,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}
	if len(groups) > 0 {
		payload["groups"] = groups
	}
	if nonce != "" {
		payload["nonce"] = nonce
	}
	hb, _ := json.Marshal(header)
	pb, _ := json.Marshal(payload)
	signingInput := b64(hb) + "." + b64(pb)
	sum := sha256.Sum256([]byte(signingInput))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
func randStr() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(v)
}
