package controlplane

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"google.golang.org/grpc"

	"github.com/atlasctl/atlas/internal/channelpb"
	"github.com/atlasctl/atlas/pkg/api"
)

// agentChannel implementa el servicio gRPC AgentChannel: el stream bidireccional
// de larga vida con cada agente. Comparte store, métricas y hub con el Server
// HTTP — es OTRO transporte para la misma sesión de agente, no otra semántica:
//   - hello    ≙ POST /v1/agents/register
//   - snapshot ≙ POST /v1/agents/{id}/heartbeat
//   - y la mejora: las acciones se EMPUJAN al encolarse, sin esperar al latido.
type agentChannel struct {
	channelpb.UnimplementedAgentChannelServer
	srv *Server
}

// GRPC construye el servidor gRPC del canal de agentes, listo para multiplexar
// junto a la API HTTP (ver MixedHandler).
func (s *Server) GRPC() *grpc.Server {
	g := grpc.NewServer()
	channelpb.RegisterAgentChannelServer(g, &agentChannel{srv: s})
	return g
}

// MixedHandler sirve gRPC y la API HTTP por el MISMO puerto: las peticiones
// HTTP/2 con content-type application/grpc van al canal de agentes y el resto a
// la API REST. Así los agentes gRPC usan el mismo :8080, la misma mTLS y las
// mismas NetworkPolicy que ya existen — cero cambios de despliegue.
func MixedHandler(grpcServer *grpc.Server, rest http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		rest.ServeHTTP(w, r)
	})
}

// Connect atiende el stream de UN agente durante toda su vida. Protocolo:
// el primer mensaje debe ser Hello (registro); después, una goroutine lectora
// procesa snapshots/resultados mientras este bucle escritor empuja acciones
// cuando el hub avisa (o tras cada snapshot, como barrido de respaldo).
func (c *agentChannel) Connect(stream channelpb.AgentChannel_ConnectServer) error {
	s := c.srv

	// 1) Registro: el primer mensaje del stream debe ser Hello.
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return errors.New("el primer mensaje del stream debe ser hello")
	}
	if hello.ClusterId == "" || hello.Name == "" {
		return errors.New("hello requiere cluster_id y name")
	}
	now := time.Now()
	token, err := s.store.Register(api.RegisterRequest{
		ClusterID:    hello.ClusterId,
		Name:         hello.Name,
		Provider:     api.Provider(hello.Provider),
		AgentVersion: hello.AgentVersion,
	}, now)
	if err != nil {
		log.Printf("grpc: error registrando %q: %v", hello.ClusterId, err)
		return errors.New("no se pudo registrar")
	}
	s.metrics.Registers.Add(1)
	if err := stream.Send(&channelpb.ServerMessage{Msg: &channelpb.ServerMessage_Ack{
		Ack: &channelpb.Ack{SnapshotIntervalSeconds: int32(s.heartbeatInterval)},
	}}); err != nil {
		return err
	}
	id := hello.ClusterId
	log.Printf("grpc: clúster %q (%s) conectado por stream", hello.Name, id)
	s.metrics.AgentStreams.Add(1)
	defer func() {
		s.metrics.AgentStreams.Add(-1)
		log.Printf("grpc: clúster %q desconectado", id)
	}()

	// 2) Timbre del hub: nos suscribimos ANTES de leer nada para no perder
	//    acciones encoladas durante el arranque.
	wake := s.hub.subscribe(id)
	defer s.hub.unsubscribe(id, wake)

	// 3) Goroutine lectora: snapshots y resultados hacia el store. Cualquier
	//    error de sesión termina el stream (el agente reconecta y re-registra).
	readErr := make(chan error, 1)
	go func() {
		for {
			in, err := stream.Recv()
			if err != nil {
				readErr <- err
				return
			}
			switch msg := in.Msg.(type) {
			case *channelpb.AgentMessage_SnapshotJson:
				var snap api.Snapshot
				if err := json.Unmarshal(msg.SnapshotJson, &snap); err != nil {
					readErr <- fmt.Errorf("snapshot ilegible: %w", err)
					return
				}
				if err := s.store.Heartbeat(id, token, snap, time.Now()); err != nil {
					s.metrics.HeartbeatErrors.Add(1)
					readErr <- fmt.Errorf("snapshot rechazado: %w", err)
					return
				}
				s.metrics.Heartbeats.Add(1)
				// Barrido de respaldo: por si una señal del hub se perdió
				// (p. ej. la acción se encoló en otra réplica).
				select {
				case wake <- struct{}{}:
				default:
				}
			case *channelpb.AgentMessage_ResultJson:
				var res api.ActionResult
				if err := json.Unmarshal(msg.ResultJson, &res); err != nil {
					readErr <- fmt.Errorf("resultado ilegible: %w", err)
					return
				}
				if err := s.store.RecordResults(id, []api.ActionResult{res}, time.Now()); err != nil {
					log.Printf("grpc: registrando resultado de %q: %v", id, err)
				}
			default:
				readErr <- errors.New("mensaje inesperado en el stream")
				return
			}
		}
	}()

	// 4) Bucle escritor (único que llama a Send tras el ack): cuando el hub
	//    avisa, recoge las acciones pendientes y las EMPUJA por el stream.
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case err := <-readErr:
			if errors.Is(err, ErrUnknownCluster) || errors.Is(err, ErrBadToken) {
				return err // sesión inválida: que el agente re-registre
			}
			return err
		case <-wake:
			actions, err := s.store.TakeActions(id, time.Now())
			if err != nil {
				log.Printf("grpc: recogiendo acciones de %q: %v", id, err)
				continue
			}
			for _, act := range actions {
				buf, err := json.Marshal(act)
				if err != nil {
					log.Printf("grpc: serializando acción %s: %v", act.ID, err)
					continue
				}
				if err := stream.Send(&channelpb.ServerMessage{
					Msg: &channelpb.ServerMessage_ActionJson{ActionJson: buf},
				}); err != nil {
					return err
				}
				log.Printf("grpc: acción %s empujada a %q al instante", act.ID, id)
			}
		}
	}
}
