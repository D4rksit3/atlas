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
	"syscall"
	"time"

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
	flag.Parse()

	store := controlplane.NewStore(*offline)
	srv := controlplane.NewServer(store, *heartbeat, *corsOrigin)

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

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
