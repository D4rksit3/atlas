// Command agent corre dentro de cada clúster gestionado. Marca hacia casa:
// abre una conexión saliente al control plane, se registra y late con el estado
// del clúster. No abre puertos de entrada.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/atlasctl/atlas/internal/agent"
	"github.com/atlasctl/atlas/pkg/api"
)

func main() {
	controlPlane := flag.String("control-plane", envOr("ATLAS_CONTROL_PLANE", "http://localhost:8080"), "URL del control plane")
	name := flag.String("name", envOr("ATLAS_CLUSTER_NAME", "on-prem lab"), "nombre legible del clúster")
	clusterID := flag.String("cluster-id", os.Getenv("ATLAS_CLUSTER_ID"), "id estable del clúster (por defecto: slug del nombre)")
	provider := flag.String("provider", envOr("ATLAS_PROVIDER", "onprem"), "onprem | aws | oci")
	collectorMode := flag.String("collector", envOr("ATLAS_COLLECTOR", "sample"), "sample (datos ficticios) | kube (clúster real vía client-go)")
	kubeconfig := flag.String("kubeconfig", os.Getenv("KUBECONFIG"), "ruta al kubeconfig (modo kube; vacío = in-cluster o ~/.kube/config)")
	workers := flag.Int("sample-workers", 3, "nº de nodos worker en el colector de ejemplo")
	flag.Parse()

	id := *clusterID
	if id == "" {
		id = slug(*name)
	}

	cfg := agent.Config{
		ControlPlaneURL: strings.TrimRight(*controlPlane, "/"),
		ClusterID:       id,
		Name:            *name,
		Provider:        api.Provider(*provider),
	}

	// Selección de colector: 'kube' lee un clúster real; 'sample' datos ficticios.
	var collector agent.Collector
	switch *collectorMode {
	case "kube":
		kc, err := agent.NewKubeCollector(*kubeconfig)
		if err != nil {
			log.Fatalf("no pude inicializar el colector kube: %v", err)
		}
		collector = kc
		log.Printf("colector: kube (leyendo un clúster real)")
	case "sample":
		collector = agent.NewSampleCollector(cfg.Provider, *workers)
		log.Printf("colector: sample (datos de ejemplo)")
	default:
		log.Fatalf("colector desconocido %q (usa: sample | kube)", *collectorMode)
	}

	a := agent.New(cfg, collector)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
		<-stop
		cancel()
	}()

	log.Printf("agente %s arrancando (clúster=%q id=%q provider=%s)", agent.Version, cfg.Name, cfg.ClusterID, cfg.Provider)
	if err := a.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("agente terminó con error: %v", err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// slug convierte "On-prem Lab" en "on-prem-lab".
func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
