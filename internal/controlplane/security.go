package controlplane

import (
	"net"
	"net/http"
	"sync"

	"golang.org/x/time/rate"
)

// ipLimiter limita peticiones por IP (token bucket). Protege el control plane de
// abuso/fuerza bruta cuando se expone a internet.
type ipLimiter struct {
	mu      sync.Mutex
	perSec  rate.Limit
	burst   int
	clients map[string]*rate.Limiter
}

const maxLimiterEntries = 50_000 // tope para no crecer sin límite

func newIPLimiter(perSec float64, burst int) *ipLimiter {
	return &ipLimiter{
		perSec:  rate.Limit(perSec),
		burst:   burst,
		clients: make(map[string]*rate.Limiter),
	}
}

func (l *ipLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Reinicio crudo si el mapa crece demasiado (evita fuga de memoria).
	if len(l.clients) > maxLimiterEntries {
		l.clients = make(map[string]*rate.Limiter)
	}
	lim, ok := l.clients[ip]
	if !ok {
		lim = rate.NewLimiter(l.perSec, l.burst)
		l.clients[ip] = lim
	}
	return lim.Allow()
}

// withRateLimit rechaza con 429 si una IP supera su cuota. Si el limitador es nil,
// no hace nada.
func (s *Server) withRateLimit(next http.Handler) http.Handler {
	if s.limiter == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.limiter.allow(clientIP(r)) {
			writeError(w, http.StatusTooManyRequests, "demasiadas peticiones, inténtalo más tarde")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withSecurityHeaders añade cabeceras de seguridad a todas las respuestas.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// HSTS solo tiene sentido bajo HTTPS (no lo fuerces en desarrollo HTTP).
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP extrae la IP del cliente (sin puerto). Nota: detrás de un proxy/Ingress
// conviene confiar en X-Forwarded-For de un proxy de confianza.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// primera IP de la cadena
		if i := indexByte(xff, ','); i >= 0 {
			return trimSpace(xff[:i])
		}
		return trimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
