package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/atlasctl/atlas/internal/channelpb"
	"github.com/atlasctl/atlas/pkg/api"
)

// RunGRPC es el transporte preferido: en vez de latir por HTTP y recoger las
// órdenes en la respuesta (hasta N segundos de espera), abre UN stream gRPC
// saliente de larga vida por el que los snapshots suben periódicamente y las
// acciones BAJAN AL INSTANTE, en cuanto la GUI las encola. Mismo puerto, misma
// mTLS y mismo sentido de conexión (el agente marca hacia casa) que HTTP.
// Bloquea hasta que el contexto se cancele; si el stream cae, reconecta con
// backoff y se re-registra (el control plane conserva el último snapshot).
func (a *Agent) RunGRPC(ctx context.Context) error {
	target, creds, err := a.dialTarget()
	if err != nil {
		return err
	}
	backoff := time.Second
	for {
		start := time.Now()
		err := a.runStream(ctx, target, creds)
		if ctx.Err() != nil {
			log.Println("agente detenido")
			return nil
		}
		// Si el stream vivió un rato, la conexión "funcionaba": empieza de cero.
		if time.Since(start) > 30*time.Second {
			backoff = time.Second
		}
		log.Printf("stream caído (%v); reconecto en %s", err, backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// dialTarget traduce la URL del control plane (http(s)://host[:puerto]) al
// target host:puerto de gRPC y a las credenciales de transporte: el MISMO
// tls.Config de mTLS que usa HTTP, o texto plano (h2c) en desarrollo.
func (a *Agent) dialTarget() (string, credentials.TransportCredentials, error) {
	u, err := url.Parse(a.cfg.ControlPlaneURL)
	if err != nil {
		return "", nil, fmt.Errorf("URL del control plane inválida: %w", err)
	}
	host := u.Hostname()
	port := u.Port()
	if host == "" {
		return "", nil, fmt.Errorf("URL del control plane sin host: %q", a.cfg.ControlPlaneURL)
	}
	if a.cfg.TLSConfig != nil {
		if port == "" {
			port = "443"
		}
		return host + ":" + port, credentials.NewTLS(a.cfg.TLSConfig), nil
	}
	if port == "" {
		port = "80"
	}
	return host + ":" + port, insecure.NewCredentials(), nil
}

// runStream atiende UNA vida del stream: registra, late y ejecuta lo que llegue.
func (a *Agent) runStream(ctx context.Context, target string, creds credentials.TransportCredentials) error {
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(creds))
	if err != nil {
		return err
	}
	defer conn.Close()

	stream, err := channelpb.NewAgentChannelClient(conn).Connect(ctx)
	if err != nil {
		return err
	}

	// 1) Registro: hello y esperamos el ack (trae la cadencia de snapshots).
	err = stream.Send(&channelpb.AgentMessage{Msg: &channelpb.AgentMessage_Hello{Hello: &channelpb.Hello{
		ClusterId:    a.cfg.ClusterID,
		Name:         a.cfg.Name,
		Provider:     string(a.cfg.Provider),
		AgentVersion: Version,
	}}})
	if err != nil {
		return err
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	ack := first.GetAck()
	if ack == nil {
		return errors.New("esperaba un ack tras el hello")
	}
	if ack.SnapshotIntervalSeconds > 0 {
		a.interval = time.Duration(ack.SnapshotIntervalSeconds) * time.Second
	}
	log.Printf("conectado por gRPC a %s; snapshot cada %s (órdenes: al instante)", target, a.interval)

	// Send no admite llamadas concurrentes: la serializamos con un mutex
	// (snapshots del ticker + resultados de acciones que acaban a su aire).
	var sendMu sync.Mutex
	send := func(msg *channelpb.AgentMessage) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(msg)
	}

	// 2) Snapshots periódicos (+ resultados que quedaron pendientes de otra vida
	//    del stream). Una goroutine con ticker; el primero sale inmediato.
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sendErr := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(a.interval)
		defer ticker.Stop()
		for {
			for _, res := range a.takePendingResults() {
				if err := a.sendResult(send, res); err != nil {
					sendErr <- err
					return
				}
			}
			if err := a.sendSnapshot(send); err != nil {
				sendErr <- err
				return
			}
			select {
			case <-streamCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	// 3) Bucle receptor: cada acción que baja se ejecuta en su goroutine (una
	//    instalación puede tardar minutos y no debe frenar snapshots ni otras
	//    órdenes) y su resultado sube por el stream en cuanto termina.
	for {
		in, err := stream.Recv()
		if err != nil {
			select {
			case serr := <-sendErr:
				return serr
			default:
			}
			return err
		}
		actJSON := in.GetActionJson()
		if actJSON == nil {
			continue // mensaje desconocido: lo ignoramos (compatibilidad futura)
		}
		var act api.Action
		if err := json.Unmarshal(actJSON, &act); err != nil {
			log.Printf("acción ilegible del control plane: %v", err)
			continue
		}
		go func() {
			res := a.execute(streamCtx, act)
			if err := a.sendResult(send, res); err != nil {
				// El stream murió con el resultado en vuelo: guárdalo y la
				// próxima vida del stream lo reporta.
				a.stashResult(res)
			}
		}()
	}
}

// sendSnapshot recoge y envía un snapshot. Un fallo del COLECTOR no tumba el
// stream (se salta el snapshot, como en HTTP); un fallo de ENVÍO sí.
func (a *Agent) sendSnapshot(send func(*channelpb.AgentMessage) error) error {
	snap, err := a.collector.Collect()
	if err != nil {
		log.Printf("colector falló, salto este snapshot: %v", err)
		return nil
	}
	buf, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return send(&channelpb.AgentMessage{Msg: &channelpb.AgentMessage_SnapshotJson{SnapshotJson: buf}})
}

func (a *Agent) sendResult(send func(*channelpb.AgentMessage) error, res api.ActionResult) error {
	buf, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return send(&channelpb.AgentMessage{Msg: &channelpb.AgentMessage_ResultJson{ResultJson: buf}})
}

// stashResult / takePendingResults guardan resultados que no pudieron enviarse
// (stream caído) para reportarlos en la siguiente conexión.
func (a *Agent) stashResult(res api.ActionResult) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pendingResults = append(a.pendingResults, res)
}

func (a *Agent) takePendingResults() []api.ActionResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	res := a.pendingResults
	a.pendingResults = nil
	return res
}
