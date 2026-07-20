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
)

func main() {
	addr := flag.String("addr", envOr("ATLAS_ADDR", ":8080"), "dirección de escucha")
	heartbeat := flag.Int("heartbeat", 10, "segundos entre latidos que se piden al agente")
	offline := flag.Duration("offline-after", 30*time.Second, "tiempo sin latidos para marcar un clúster offline")
	flag.Parse()

	store := controlplane.NewStore(*offline)
	srv := controlplane.NewServer(store, *heartbeat)

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
	}

	// Arranque en goroutine para poder hacer shutdown ordenado.
	go func() {
		log.Printf("control plane escuchando en %s", *addr)
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
