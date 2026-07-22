// Login local integrado: usuario/contraseña propios de Atlas, sin depender de
// un IdP externo. Pensado para que la plataforma NUNCA quede abierta: el
// instalador crea el admin y la GUI exige sesión desde el primer arranque.
//
// La contraseña se guarda como hash bcrypt (nunca en claro en memoria más allá
// del arranque). La sesión es un token firmado con HMAC-SHA256 y caducidad
// corta: sin estado en el servidor, funciona igual con una réplica o con varias
// (compartiendo ATLAS_SESSION_KEY).
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Local autentica con el usuario/contraseña integrados de Atlas.
type Local struct {
	username string
	hash     []byte // bcrypt de la contraseña
	key      []byte // clave HMAC de las sesiones
	ttl      time.Duration
}

// ErrBadCredentials cubre usuario o contraseña incorrectos — a propósito
// indistinguibles para quien lo intenta.
var ErrBadCredentials = errors.New("usuario o contraseña incorrectos")

// NewLocal construye el login local. password puede venir en claro (se hashea
// aquí y se descarta) o ya como hash bcrypt ($2a$...). sessionKey vacía genera
// una aleatoria (válida con UNA réplica; con varias, compártela vía Secret).
func NewLocal(username, password string, sessionKey []byte, ttl time.Duration) (*Local, error) {
	if username == "" || password == "" {
		return nil, errors.New("login local: hacen falta usuario y contraseña")
	}
	var hash []byte
	if strings.HasPrefix(password, "$2a$") || strings.HasPrefix(password, "$2b$") || strings.HasPrefix(password, "$2y$") {
		// Ya es un hash bcrypt: comprueba que es válido y úsalo tal cual.
		if _, err := bcrypt.Cost([]byte(password)); err != nil {
			return nil, fmt.Errorf("login local: el hash bcrypt no es válido: %w", err)
		}
		hash = []byte(password)
	} else {
		h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("login local: hasheando la contraseña: %w", err)
		}
		hash = h
	}
	if len(sessionKey) == 0 {
		sessionKey = make([]byte, 32)
		if _, err := rand.Read(sessionKey); err != nil {
			return nil, fmt.Errorf("login local: generando clave de sesión: %w", err)
		}
	}
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	return &Local{username: username, hash: hash, key: sessionKey, ttl: ttl}, nil
}

// sessionClaims es el contenido firmado de un token de sesión local.
type sessionClaims struct {
	User string `json:"u"`
	Exp  int64  `json:"exp"` // epoch segundos
}

// Login verifica las credenciales y, si son correctas, emite un token de sesión
// firmado con su caducidad.
func (l *Local) Login(username, password string) (token string, exp time.Time, err error) {
	// Comparaciones en tiempo constante: ni el usuario ni la contraseña filtran
	// cuál de los dos falló.
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(l.username)) == 1
	passErr := bcrypt.CompareHashAndPassword(l.hash, []byte(password))
	if !userOK || passErr != nil {
		return "", time.Time{}, ErrBadCredentials
	}
	exp = time.Now().Add(l.ttl)
	payload, _ := json.Marshal(sessionClaims{User: l.username, Exp: exp.Unix()})
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + l.sign(body), exp, nil
}

// Verify comprueba un token de sesión local: firma válida y no caducado.
func (l *Local) Verify(token string) (User, bool) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return User{}, false
	}
	if subtle.ConstantTimeCompare([]byte(sig), []byte(l.sign(body))) != 1 {
		return User{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return User{}, false
	}
	var c sessionClaims
	if err := json.Unmarshal(raw, &c); err != nil {
		return User{}, false
	}
	if time.Now().Unix() >= c.Exp {
		return User{}, false
	}
	// El admin local siempre es operador: es quien instaló la plataforma.
	return User{Subject: "local:" + c.User, Email: c.User, Role: RoleOperator}, true
}

func (l *Local) sign(body string) string {
	mac := hmac.New(sha256.New, l.key)
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Username devuelve el nombre del admin local (para logs).
func (l *Local) Username() string { return l.username }
