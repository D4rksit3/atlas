// Command controlplane es el plano de control self-hosted de Atlas: recibe
// registros y latidos de los agentes y expone la topología a la GUI.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/atlasctl/atlas/internal/auth"
	"github.com/atlasctl/atlas/internal/controlplane"
	"github.com/atlasctl/atlas/internal/mtls"
)

func main() {
	addr := flag.String("addr", envOr("ATLAS_ADDR", ":8080"), "dirección de escucha")
	heartbeat := flag.Int("heartbeat", 10, "segundos entre latidos que se piden al agente")
	offline := flag.Duration("offline-after", 30*time.Second, "tiempo sin latidos para marcar un clúster offline")
	corsOrigin := flag.String("cors-origin", envOr("ATLAS_CORS_ORIGIN", "*"), "origen permitido para la GUI (usa el dominio concreto en producción)")
	tlsCert := flag.String("tls-cert", os.Getenv("ATLAS_TLS_CERT"), "certificado del servidor (activa mTLS junto con --tls-key y --tls-client-ca)")
	tlsKey := flag.String("tls-key", os.Getenv("ATLAS_TLS_KEY"), "clave privada del servidor")
	tlsClientCA := flag.String("tls-client-ca", os.Getenv("ATLAS_TLS_CLIENT_CA"), "CA que firma los certificados de los agentes (para verificarlos)")
	tlsCRL := flag.String("tls-crl", os.Getenv("ATLAS_TLS_CRL"), "CRL firmada por la CA: rechaza agentes revocados en el acto (opcional; recarga en caliente)")
	storeKind := flag.String("store", envOr("ATLAS_STORE", "memory"), "memory (una réplica, volátil) | postgres (persistente, multi-réplica)")
	pgDSN := flag.String("postgres-dsn", os.Getenv("ATLAS_POSTGRES_DSN"), "DSN de Postgres (postgres://user:pass@host:5432/db) para --store=postgres")
	oidcIssuer := flag.String("oidc-issuer", os.Getenv("ATLAS_OIDC_ISSUER"), "URL del IdP OIDC (activa la auth de la GUI). Vacío = sin auth (solo desarrollo)")
	oidcClientID := flag.String("oidc-client-id", os.Getenv("ATLAS_OIDC_CLIENT_ID"), "client id (audiencia) de la GUI en el IdP")
	rbacOperators := flag.String("rbac-operators", os.Getenv("ATLAS_RBAC_OPERATORS"), "emails o grupos (coma-separados) que pueden OPERAR (escalar/reiniciar)")
	adminUser := flag.String("admin-user", envOr("ATLAS_ADMIN_USER", "admin"), "usuario del login local integrado")
	adminPassword := flag.String("admin-password", os.Getenv("ATLAS_ADMIN_PASSWORD"), "contraseña del login local (en claro o hash bcrypt $2a$...). Activa el login local. Vacío = sin login local")
	sessionKey := flag.String("session-key", os.Getenv("ATLAS_SESSION_KEY"), "clave HMAC de las sesiones del login local (compártela entre réplicas). Vacío = aleatoria por proceso")
	sessionTTL := flag.Duration("session-ttl", envDurationOr("ATLAS_SESSION_TTL", 12*time.Hour), "vida de una sesión del login local")
	rateLimit := flag.Float64("rate-limit", 20, "peticiones/segundo por IP (0 = sin límite)")
	alertWebhook := flag.String("alert-webhook", os.Getenv("ATLAS_ALERT_WEBHOOK"), "URL a la que POSTear las alertas (JSON) cuando aparecen o se resuelven. Vacío = solo panel")
	flag.Parse()

	store, closeStore := buildStore(*storeKind, *pgDSN, *offline)
	defer closeStore()
	authn := buildAuth(*oidcIssuer, *oidcClientID, *rbacOperators,
		*adminUser, *adminPassword, *sessionKey, *sessionTTL)
	if authn != nil {
		// Los usuarios creados desde la GUI (almacén) también pueden iniciar sesión.
		authn.ConnectUserStore(store.UserAuth)
	}
	srv := controlplane.NewServer(store, *heartbeat, *corsOrigin, authn)
	srv.SetRateLimit(*rateLimit, int(*rateLimit*2))

	// Vigilante de alertas: evalúa cada 30s y notifica flancos por webhook.
	alerter := controlplane.NewAlerter(store, *alertWebhook)
	srv.SetAlerter(alerter)
	go alerter.Run(context.Background(), 30*time.Second)
	if *alertWebhook != "" {
		log.Printf("alertas: webhook configurado (%s)", *alertWebhook)
	}

	// Un solo puerto para todo: las peticiones gRPC (streams de agentes) van al
	// canal bidireccional y el resto a la API REST. Así gRPC hereda la misma
	// mTLS y las mismas NetworkPolicy sin cambios de despliegue.
	handler := controlplane.MixedHandler(srv.GRPC(), srv.Routes())

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		// Sin WriteTimeout: los streams gRPC viven horas y un timeout de escritura
		// los cortaría. Los handlers REST son cortos por diseño.
	}

	// mTLS opcional: si se dan los tres ficheros, exigimos certificado de cliente
	// válido a cada agente. Sin ellos, HTTP plano (solo para desarrollo local).
	mtlsOn := *tlsCert != "" && *tlsKey != "" && *tlsClientCA != ""
	if !mtlsOn && (*tlsCert != "" || *tlsKey != "" || *tlsClientCA != "") {
		log.Fatalf("para mTLS hacen falta los tres: --tls-cert, --tls-key y --tls-client-ca")
	}
	if mtlsOn {
		tlsCfg, err := mtls.ServerTLSConfig(*tlsCert, *tlsKey, *tlsClientCA, *tlsCRL)
		if err != nil {
			log.Fatalf("configurando mTLS: %v", err)
		}
		httpServer.TLSConfig = tlsCfg
		if *tlsCRL != "" {
			log.Printf("revocación activa: compruebo cada agente contra la CRL %s (recarga en caliente)", *tlsCRL)
		}
	} else {
		// Sin TLS (desarrollo) el HTTP/2 no se negocia solo (eso lo hace ALPN en
		// el handshake TLS); h2c lo habilita en claro para que gRPC funcione.
		httpServer.Handler = h2c.NewHandler(handler, &http2.Server{})
	}

	// Arranque en goroutine para poder hacer shutdown ordenado.
	go func() {
		if mtlsOn {
			log.Printf("control plane escuchando en %s (mTLS: se exige certificado de cliente)", *addr)
			// Los certs ya están en TLSConfig; ListenAndServeTLS admite rutas vacías.
			if err := httpServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				log.Fatalf("servidor caído: %v", err)
			}
			return
		}
		log.Printf("control plane escuchando en %s (HTTP, sin mTLS — solo desarrollo)", *addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("servidor caído: %v", err)
		}
	}()

	// Espera SIGINT/SIGTERM y cierra con gracia.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("apagando…")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("shutdown con error: %v", err)
	}
}

// buildStore elige el backend de almacenamiento. Devuelve el store y una función
// de cierre (no-op para memoria; cierra el pool para Postgres).
func buildStore(kind, dsn string, offline time.Duration) (controlplane.Store, func()) {
	switch kind {
	case "memory":
		log.Printf("store: memory (una réplica, se pierde al reiniciar)")
		return controlplane.NewMemStore(offline), func() {}
	case "postgres":
		if dsn == "" {
			log.Fatalf("--store=postgres requiere --postgres-dsn (o ATLAS_POSTGRES_DSN)")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		pg, err := controlplane.NewPgStore(ctx, dsn, offline)
		if err != nil {
			log.Fatalf("store postgres: %v", err)
		}
		log.Printf("store: postgres (persistente, multi-réplica)")
		return pg, pg.Close
	default:
		log.Fatalf("store desconocido %q (usa: memory | postgres)", kind)
		return nil, func() {}
	}
}

// buildAuth compone la autenticación de la GUI: login local integrado (si hay
// contraseña de admin), OIDC (si hay issuer), o ambos. Sin ninguno devuelve nil
// (auth deshabilitada, solo para desarrollo).
func buildAuth(issuer, clientID, operators, adminUser, adminPassword, sessionKey string, sessionTTL time.Duration) *auth.Authenticator {
	var a *auth.Authenticator

	if issuer != "" {
		if clientID == "" {
			log.Fatalf("--oidc-issuer requiere también --oidc-client-id")
		}
		var ops []string
		for _, o := range strings.Split(operators, ",") {
			if o = strings.TrimSpace(o); o != "" {
				ops = append(ops, o)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		oa, err := auth.New(ctx, auth.Config{Issuer: issuer, ClientID: clientID, Operators: ops})
		if err != nil {
			log.Fatalf("auth OIDC: %v", err)
		}
		log.Printf("auth: OIDC activa (issuer=%s, %d operador(es))", issuer, len(ops))
		a = oa
	}

	if adminPassword != "" {
		local, err := auth.NewLocal(adminUser, adminPassword, []byte(sessionKey), sessionTTL)
		if err != nil {
			log.Fatalf("auth local: %v", err)
		}
		if sessionKey == "" {
			log.Printf("auth: clave de sesión aleatoria (las sesiones caducan al reiniciar; con varias réplicas define ATLAS_SESSION_KEY)")
		}
		if a == nil {
			a = auth.NewLocalOnly(local)
		} else {
			a.SetLocal(local)
		}
		log.Printf("auth: login local activo (usuario %q, sesiones de %s)", adminUser, sessionTTL)
	}

	if a == nil {
		log.Printf("auth: DESHABILITADA (ni --admin-password ni --oidc-issuer) — no expongas esto a internet")
	}
	return a
}

// envDurationOr lee una duración de una variable de entorno ("12h", "30m").
func envDurationOr(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Fatalf("%s: duración inválida %q (ej: 12h, 30m)", key, v)
	}
	return d
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
