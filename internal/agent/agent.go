package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/atlasctl/atlas/pkg/api"
)

// Version del agente; sobreescríbela en el build con -ldflags si quieres.
var Version = "0.1.0-dev"

// Config parametriza el agente.
type Config struct {
	ControlPlaneURL string       // ej. http://localhost:8080
	ClusterID       string       // identificador estable del clúster
	Name            string       // nombre legible
	Provider        api.Provider // onprem | aws | oci
}

// Agent marca hacia casa: se registra y luego late periódicamente. NUNCA abre
// puertos de entrada — es el clúster quien inicia la conexión saliente.
type Agent struct {
	cfg       Config
	collector Collector
	http      *http.Client

	token    string
	interval time.Duration
}

// New construye un agente con un colector dado.
func New(cfg Config, collector Collector) *Agent {
	return &Agent{
		cfg:       cfg,
		collector: collector,
		http:      &http.Client{Timeout: 10 * time.Second},
		interval:  10 * time.Second,
	}
}

// Run bloquea hasta que el contexto se cancele. Registra, luego late.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.registerWithRetry(ctx); err != nil {
		return err
	}

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	// Primer latido inmediato para no esperar el primer tick.
	a.beat(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Println("agente detenido")
			return nil
		case <-ticker.C:
			a.beat(ctx)
		}
	}
}

func (a *Agent) beat(ctx context.Context) {
	if err := a.heartbeat(ctx); err != nil {
		log.Printf("latido fallido: %v", err)
		// Si el control plane nos olvidó (reinició), reintenta registrarse.
		if err == errNeedsReregister {
			if rerr := a.registerWithRetry(ctx); rerr != nil {
				log.Printf("re-registro fallido: %v", rerr)
			}
		}
	}
}

func (a *Agent) registerWithRetry(ctx context.Context) error {
	backoff := time.Second
	for {
		err := a.register(ctx)
		if err == nil {
			return nil
		}
		log.Printf("registro fallido (%v); reintento en %s", err, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (a *Agent) register(ctx context.Context) error {
	body := api.RegisterRequest{
		ClusterID:    a.cfg.ClusterID,
		Name:         a.cfg.Name,
		Provider:     a.cfg.Provider,
		AgentVersion: Version,
	}
	var resp api.RegisterResponse
	if err := a.post(ctx, "/v1/agents/register", body, &resp); err != nil {
		return err
	}
	a.token = resp.Token
	if resp.HeartbeatIntervalSeconds > 0 {
		a.interval = time.Duration(resp.HeartbeatIntervalSeconds) * time.Second
	}
	log.Printf("registrado en %s; latido cada %s", a.cfg.ControlPlaneURL, a.interval)
	return nil
}

var errNeedsReregister = fmt.Errorf("el control plane pide re-registro")

func (a *Agent) heartbeat(ctx context.Context) error {
	hb := api.Heartbeat{Token: a.token, Snapshot: a.collector.Collect()}
	path := "/v1/agents/" + a.cfg.ClusterID + "/heartbeat"
	return a.post(ctx, path, hb, nil)
}

// post hace un POST JSON y decodifica la respuesta en out (si out != nil).
func (a *Agent) post(ctx context.Context, path string, in, out any) error {
	buf, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.ControlPlaneURL+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	// 404/401 en un latido significan que hay que volver a registrarse.
	if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusUnauthorized {
		return errNeedsReregister
	}
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("HTTP %d: %s", res.StatusCode, bytes.TrimSpace(msg))
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}
