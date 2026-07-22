package controlplane

import "sync"

// hub es el timbre que conecta la API de la GUI con los streams gRPC: cuando
// alguien encola una acción para un clúster, notify() despierta al stream de
// ese clúster para que recoja y empuje la orden AL INSTANTE (sin esperar al
// siguiente snapshot).
//
// La señal no lleva datos (struct{}): solo dice "hay algo para ti, mira el
// store". Así el store sigue siendo la única fuente de verdad y perder una
// señal no pierde la acción (el barrido periódico del snapshot la recoge).
//
// Nota multi-réplica: el hub es EN MEMORIA. Con varias réplicas y Postgres, la
// señal solo llega si la GUI y el stream del agente cayeron en la misma
// réplica; si no, la acción sale en el siguiente snapshot (como con HTTP).
// Empujar entre réplicas (LISTEN/NOTIFY de Postgres) queda como mejora.
type hub struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{} // clusterID -> suscriptores
}

func newHub() *hub {
	return &hub{subs: make(map[string]map[chan struct{}]struct{})}
}

// subscribe registra un oyente para un clúster y devuelve su canal de señal
// (capacidad 1: las señales colapsan, no se acumulan).
func (h *hub) subscribe(clusterID string) chan struct{} {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[clusterID] == nil {
		h.subs[clusterID] = make(map[chan struct{}]struct{})
	}
	h.subs[clusterID][ch] = struct{}{}
	return ch
}

// unsubscribe retira un oyente (al cerrarse su stream).
func (h *hub) unsubscribe(clusterID string, ch chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs[clusterID], ch)
	if len(h.subs[clusterID]) == 0 {
		delete(h.subs, clusterID)
	}
}

// notify despierta a los oyentes de un clúster. Nunca bloquea: si el canal ya
// tiene una señal pendiente, con esa basta (el oyente mirará el store).
func (h *hub) notify(clusterID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[clusterID] {
		select {
		case ch <- struct{}{}:
		default: // ya tenía señal pendiente
		}
	}
}
