package server

import (
	"sync"

	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/events"
)

// Hub reparte los eventos que llegan del bus entre los WebSocket abiertos en
// ESTA instancia. Lo que cruza de una instancia a otra es cosa del bus; el hub
// solo se ocupa de la ultima milla.
//
// Los suscriptores se agrupan por libro: un lector abre la ficha de un libro y
// solo le importa lo que pase ahi. Asi un evento se compara contra un mapa en
// vez de recorrer todas las conexiones abiertas.
type Hub struct {
	mu sync.RWMutex
	// byBook es libro -> conjunto de canales escuchandolo. El valor es un
	// conjunto y no una lista porque hay que poder quitar uno por el medio
	// cuando se cierra, y eso en una lista obliga a recorrerla entera.
	byBook map[string]map[chan events.Event]struct{}
}

func NewHub() *Hub {
	return &Hub{byBook: make(map[string]map[chan events.Event]struct{})}
}

// Subscribe registra un oyente para un libro y devuelve su canal junto con la
// funcion que lo da de baja. El canal tiene buffer para que un cliente lento no
// bloquee al que publica; si se llena igual, Broadcast descarta.
func (h *Hub) Subscribe(bookID string) (<-chan events.Event, func()) {
	ch := make(chan events.Event, 16)

	h.mu.Lock()
	if h.byBook[bookID] == nil {
		h.byBook[bookID] = make(map[chan events.Event]struct{})
	}
	h.byBook[bookID][ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		// once porque el desregistro se llama desde el defer del handler y
		// tambien cuando la escritura falla: hacerlo dos veces cerraria un canal
		// ya cerrado, que es un panic.
		once.Do(func() {
			h.mu.Lock()
			if subs := h.byBook[bookID]; subs != nil {
				delete(subs, ch)
				// El mapa del libro se borra cuando se va el ultimo: si no,
				// quedarian entradas vacias de cada libro que alguien miro
				// alguna vez.
				if len(subs) == 0 {
					delete(h.byBook, bookID)
				}
			}
			h.mu.Unlock()
			close(ch)
		})
	}

	return ch, unsubscribe
}

// Broadcast entrega el evento a los oyentes de ese libro.
//
// El envio no espera: si un cliente tiene el buffer lleno, se le descarta ese
// evento en vez de frenar a todos los demas. Es la decision correcta para lo
// que se manda aqui —contadores y comentarios—, donde el que se quedo atras se
// pone al dia recargando por REST; bloquear el reparto por una conexion lenta
// afectaria a toda la instancia.
func (h *Hub) Broadcast(evt events.Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for ch := range h.byBook[evt.BookID] {
		select {
		case ch <- evt:
		default:
		}
	}
}

// Run bombea del bus al hub hasta que se cierre el canal (al terminar ctx).
func (h *Hub) Run(in <-chan events.Event) {
	for evt := range in {
		h.Broadcast(evt)
	}
}

// Subscribers es cuantos oyentes hay ahora mismo para un libro. Existe para las
// pruebas y para el log: es la forma de comprobar que las conexiones cerradas
// se dan de baja y no se acumulan.
func (h *Hub) Subscribers(bookID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.byBook[bookID])
}
