package server

import (
	"sync"
	"testing"
	"time"

	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/events"
)

// El hub es lo unico de este servicio que varias goroutines tocan a la vez: los
// handlers de WebSocket entran y salen mientras el bombeo del bus reparte. Estas
// pruebas cubren eso; correrlas con -race es donde de verdad valen.

const (
	libroA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	libroB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

func recibir(t *testing.T, ch <-chan events.Event) events.Event {
	t.Helper()
	select {
	case evt := <-ch:
		return evt
	case <-time.After(time.Second):
		t.Fatal("no llego el evento")
		return events.Event{}
	}
}

func TestHub_EntregaSoloALosDelMismoLibro(t *testing.T) {
	hub := NewHub()

	deA, cerrarA := hub.Subscribe(libroA)
	defer cerrarA()
	deB, cerrarB := hub.Subscribe(libroB)
	defer cerrarB()

	hub.Broadcast(events.Event{Type: events.TypeLikeChanged, BookID: libroA})

	if evt := recibir(t, deA); evt.BookID != libroA {
		t.Fatalf("bookId = %q, se esperaba %q", evt.BookID, libroA)
	}

	// El del otro libro no tiene por que enterarse.
	select {
	case evt := <-deB:
		t.Fatalf("el oyente de otro libro recibio %+v", evt)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHub_VariosOyentesDelMismoLibro(t *testing.T) {
	hub := NewHub()

	uno, cerrarUno := hub.Subscribe(libroA)
	defer cerrarUno()
	dos, cerrarDos := hub.Subscribe(libroA)
	defer cerrarDos()

	if got := hub.Subscribers(libroA); got != 2 {
		t.Fatalf("oyentes = %d, se esperaban 2", got)
	}

	hub.Broadcast(events.Event{Type: events.TypeCommentCreated, BookID: libroA})

	recibir(t, uno)
	recibir(t, dos)
}

// Al cerrarse una conexion su suscripcion tiene que desaparecer: si no, cada
// lector que pasa por un libro dejaria un canal muerto acumulandose.
func TestHub_DarseDeBajaLibera(t *testing.T) {
	hub := NewHub()

	_, cerrar := hub.Subscribe(libroA)
	cerrar()

	if got := hub.Subscribers(libroA); got != 0 {
		t.Fatalf("oyentes = %d tras darse de baja, se esperaba 0", got)
	}

	// Y el broadcast posterior no puede escribir en el canal cerrado (seria un
	// panic), que es lo que esta linea comprueba al no reventar.
	hub.Broadcast(events.Event{Type: events.TypeLikeChanged, BookID: libroA})
}

// La baja se llama desde el defer del handler y tambien cuando falla una
// escritura: tiene que aguantar que la llamen dos veces sin cerrar dos veces el
// canal.
func TestHub_DarseDeBajaDosVecesNoRevienta(t *testing.T) {
	hub := NewHub()

	_, cerrar := hub.Subscribe(libroA)
	cerrar()
	cerrar()
}

// Un cliente que dejo de leer no puede frenar al resto: se le descarta el
// evento y los demas siguen recibiendo.
func TestHub_UnOyenteLentoNoBloqueaAlResto(t *testing.T) {
	hub := NewHub()

	lento, cerrarLento := hub.Subscribe(libroA)
	defer cerrarLento()
	rapido, cerrarRapido := hub.Subscribe(libroA)
	defer cerrarRapido()

	// Se llena el buffer del lento sin leer nada de el.
	for i := 0; i < 100; i++ {
		hub.Broadcast(events.Event{Type: events.TypeLikeChanged, BookID: libroA})
		// El rapido se vacia en cada vuelta para que no se llene tambien.
		select {
		case <-rapido:
		default:
		}
	}

	// Si el descarte no funcionara, el Broadcast de arriba habria quedado
	// bloqueado y no se llegaria hasta aqui.
	_ = lento
}

// Entrar y salir mientras se reparte es el caso real: un lector cierra la
// pestana justo cuando otro comenta. Con -race, esta prueba es la que detecta
// un acceso sin proteger al mapa.
func TestHub_AltasBajasYBroadcastEnParalelo(t *testing.T) {
	hub := NewHub()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, cerrar := hub.Subscribe(libroA)
			// Se vacia el canal hasta que lo cierren, como hace el escritor real.
			go func() {
				for range ch {
				}
			}()
			time.Sleep(time.Millisecond)
			cerrar()
		}()
	}

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hub.Broadcast(events.Event{Type: events.TypeLikeChanged, BookID: libroA})
		}()
	}

	wg.Wait()

	if got := hub.Subscribers(libroA); got != 0 {
		t.Fatalf("quedaron %d oyentes tras cerrarlos todos", got)
	}
}

// Run es el puente entre el bus y el hub: lo que llega de Valkey tiene que
// terminar en los oyentes locales.
func TestHub_RunRepartLoQueLlegaDelBus(t *testing.T) {
	hub := NewHub()
	desdeElBus := make(chan events.Event, 1)

	go hub.Run(desdeElBus)

	oyente, cerrar := hub.Subscribe(libroA)
	defer cerrar()

	desdeElBus <- events.Event{Type: events.TypeRatingChanged, BookID: libroA}

	if evt := recibir(t, oyente); evt.Type != events.TypeRatingChanged {
		t.Fatalf("tipo = %q", evt.Type)
	}
	close(desdeElBus)
}
