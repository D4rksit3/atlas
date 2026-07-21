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
	storeKind := flag.String("store", envOr("ATLAS_STORE", "memory"), "memory (una réplica, volátil) | postgres (persistente, multi-réplica)")
	pgDSN := flag.String("postgres-dsn", os.Getenv("ATLAS_POSTGRES_DSN"), "DSN de Postgres (postgres://user:pass@host:5432/db) para --store=postgres")
	oidcIssuer := flag.String("oidc-issuer", os.Getenv("ATLAS_OIDC_ISSUER"), "URL del IdP OIDC (activa la auth de la GUI). Vacío = sin auth (solo desarrollo)")
	oidcClientID := flag.String("oidc-client-id", os.Getenv("ATLAS_OIDC_CLIENT_ID"), "client id (audiencia) de la GUI en el IdP")
	rbacOperators := flag.String("rbac-operators", os.Getenv("ATLAS_RBAC_OPERATORS"), "emails o grupos (coma-separados) que pueden OPERAR (escalar/reiniciar)")
	flag.Parse()

	store, closeStore := buildStore(*storeKind, *pgDSN, *offline)
	defer closeStore()
	authn := buildAuth(*oidcIssuer, *oidcClientID, *rbacOperators)
	srv := controlplane.NewServer(store, *heartbeat, *corsOrigin, authn)

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
	}

	// mTLS opcional: si se dan los tres ficheros, exigimos certificado de cliente
	// válido a cada agente. Sin ellos, HTTP plano (solo para desarrollo local).
	mtlsOn := *tlsCert != "" && *tlsKey != "" && *tlsClientCA != ""
	if !mtlsOn && (*tlsCert != "" || *tlsKey != "" || *tlsClientCA != "") {
		log.Fatalf("para mTLS hacen falta los tres: --tls-cert, --tls-key y --tls-client-ca")
	}
	if mtlsOn {
		tlsCfg, err := mtls.ServerTLSConfig(*tlsCert, *tlsKey, *tlsClientCA)
		if err != nil {
			log.Fatalf("configurando mTLS: %v", err)
		}
		httpServer.TLSConfig = tlsCfg
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

// buildAuth crea el autenticador OIDC si se configuró un issuer; si no, devuelve
// nil (auth deshabilitada, solo para desarrollo local).
func buildAuth(issuer, clientID, operators string) *auth.Authenticator {
	if issuer == "" {
		log.Printf("auth: DESHABILITADA (sin --oidc-issuer) — no expongas esto a internet")
		return nil
	}
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
	a, err := auth.New(ctx, auth.Config{Issuer: issuer, ClientID: clientID, Operators: ops})
	if err != nil {
		log.Fatalf("auth OIDC: %v", err)
	}
	log.Printf("auth: OIDC activa (issuer=%s, %d operador(es))", issuer, len(ops))
	return a
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
