package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/D4rksit3/atlas/pkg/api"
)

// Version del agente; sobreescríbela en el build con -ldflags si quieres.
var Version = "0.1.0-dev"

// Config parametriza el agente.
type Config struct {
	ControlPlaneURL string       // ej. http://localhost:8080 o https://... con mTLS
	ClusterID       string       // identificador estable del clúster
	Name            string       // nombre legible
	Provider        api.Provider // onprem | aws | oci
	// TLSConfig, si no es nil, activa mTLS: el agente presenta su certificado de
	// cliente y verifica el del control plane. Va con una URL https://.
	TLSConfig *tls.Config
}

// Agent marca hacia casa: se registra y luego late periódicamente. NUNCA abre
// puertos de entrada — es el clúster quien inicia la conexión saliente. Las
// órdenes de la GUI viajan de vuelta en la respuesta del latido.
type Agent struct {
	cfg       Config
	collector Collector
	actuator  Actuator
	http      *http.Client

	token          string
	interval       time.Duration
	mu             sync.Mutex         // protege pendingResults (en gRPC hay concurrencia)
	pendingResults []api.ActionResult // resultados a reportar en el próximo latido
}

// New construye un agente con un colector y, opcionalmente, un actuador (para
// ejecutar acciones). Si actuator es nil, el agente rechaza las acciones.
func New(cfg Config, collector Collector, actuator Actuator) *Agent {
	client := &http.Client{Timeout: 10 * time.Second}
	if cfg.TLSConfig != nil {
		client.Transport = &http.Transport{TLSClientConfig: cfg.TLSConfig}
	}
	return &Agent{
		cfg:       cfg,
		collector: collector,
		actuator:  actuator,
		http:      client,
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
	snap, err := a.collector.Collect()
	if err != nil {
		// Un fallo del colector no debe tumbar la sesión: registramos y saltamos
		// este latido. El clúster mantiene su último snapshot hasta que expire.
		log.Printf("colector falló, salto este latido: %v", err)
		return nil
	}
	pending := a.takePendingResults()
	hb := api.Heartbeat{Token: a.token, Snapshot: snap, Results: pending}
	path := "/v1/agents/" + a.cfg.ClusterID + "/heartbeat"

	var resp api.HeartbeatResponse
	if err := a.post(ctx, path, hb, &resp); err != nil {
		// Devolvemos los resultados a la cola: se reintentan en el próximo latido.
		for _, r := range pending {
			a.stashResult(r)
		}
		return err
	}

	// Ejecuta las acciones que llegaron de vuelta; sus resultados irán en el
	// próximo latido.
	for _, act := range resp.Actions {
		a.stashResult(a.execute(ctx, act))
	}
	return nil
}

// execute corre una acción con el actuador (o la rechaza si no hay actuador).
func (a *Agent) execute(ctx context.Context, act api.Action) api.ActionResult {
	if a.actuator == nil {
		return api.ActionResult{ID: act.ID, OK: false,
			Error: "este agente no puede ejecutar acciones (arráncalo con --collector kube)"}
	}
	log.Printf("ejecutando acción %s: %s %s/%s", act.ID, act.Kind, act.Namespace, act.Workload)
	res := a.actuator.Execute(ctx, act)
	if res.OK {
		log.Printf("acción %s ejecutada", act.ID)
	} else {
		log.Printf("acción %s falló: %s", act.ID, res.Error)
	}
	return res
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
