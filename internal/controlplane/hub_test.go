package controlplane

import "testing"

func TestHubNotifyDespiertaAlSuscriptor(t *testing.T) {
	h := newHub()
	ch := h.subscribe("c1")
	h.notify("c1")
	select {
	case <-ch:
	default:
		t.Fatal("el suscriptor no recibió la señal")
	}
}

func TestHubNotifyNoBloqueaConSenalPendiente(t *testing.T) {
	h := newHub()
	h.subscribe("c1")
	// Dos notify seguidos sin que nadie lea: el segundo no debe bloquear
	// (las señales colapsan en una).
	h.notify("c1")
	h.notify("c1")
}

func TestHubNotifyAClusterSinSuscriptoresEsNoop(t *testing.T) {
	newHub().notify("nadie") // no debe hacer pánico ni bloquear
}

func TestHubUnsubscribeRetiraAlOyente(t *testing.T) {
	h := newHub()
	ch := h.subscribe("c1")
	h.unsubscribe("c1", ch)
	h.notify("c1")
	select {
	case <-ch:
		t.Fatal("recibió señal tras darse de baja")
	default:
	}
}

func TestHubVariosSuscriptoresMismoCluster(t *testing.T) {
	h := newHub()
	a, b := h.subscribe("c1"), h.subscribe("c1")
	h.notify("c1")
	for name, ch := range map[string]chan struct{}{"a": a, "b": b} {
		select {
		case <-ch:
		default:
			t.Fatalf("el suscriptor %s no recibió la señal", name)
		}
	}
}
