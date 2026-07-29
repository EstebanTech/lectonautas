package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/domain"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/events"
	"github.com/EstebanTech/lectonautas/backend/shared/logx"
)

// Tiempos del keepalive. El servidor manda un ping cada pingPeriod y espera el
// pong dentro de pongWait; si no llega, da la conexion por muerta y la cierra.
//
// Sin esto, una conexion que se corta sin avisar (el tunel se cae, el movil
// pierde la red) quedaria abierta del lado del servidor consumiendo una
// suscripcion para siempre. pingPeriod es menor que pongWait a proposito: tiene
// que haber margen para al menos un intento antes de que venza el plazo.
const (
	pongWait   = 60 * time.Second
	pingPeriod = 25 * time.Second
	writeWait  = 10 * time.Second
	// Los mensajes del cliente no se usan para nada (el canal es de ida), asi
	// que se le pone un tope bajo: nadie tiene por que mandar mas que eso.
	maxClientMessage = 512
)

// Snapshotter es lo que el WebSocket necesita del servicio: el estado de
// entrada. Es una interfaz para no atar el servidor a la implementacion y poder
// probar el handler sin base de datos.
type Snapshotter interface {
	Snapshot(ctx context.Context, bookID string) (*domain.BookInteractions, error)
}

// BookChecker comprueba que el libro exista y este publicado antes de dejar que
// alguien se suscriba a el.
type BookChecker interface {
	Exists(ctx context.Context, bookID string) error
}

// TokenValidator resuelve un token de sesion. Solo se usa si el cliente manda
// uno: el stream es publico.
type TokenValidator interface {
	ValidateToken(ctx context.Context, token string) (string, error)
}

// WSHandler sirve GET /v1/ws/books/{bookId}.
//
// El canal es de ida: el servidor manda, el cliente escucha. Las escrituras van
// por REST, no por aqui. Se decidio asi porque una escritura necesita el mismo
// tratamiento que ya tiene el REST —validacion, permisos, codigos de error— y
// duplicarlo sobre un protocolo distinto seria dos caminos que pueden divergir.
type WSHandler struct {
	hub       *Hub
	snapshots Snapshotter
	books     BookChecker
	tokens    TokenValidator
	upgrader  websocket.Upgrader
}

func NewWSHandler(hub *Hub, snapshots Snapshotter, books BookChecker, tokens TokenValidator) *WSHandler {
	return &WSHandler{
		hub:       hub,
		snapshots: snapshots,
		books:     books,
		tokens:    tokens,
		upgrader: websocket.Upgrader{
			// El origen no se comprueba aqui: quien decide que navegadores
			// pueden hablar con esta API es la politica CORS del gateway, que es
			// el unico que esta expuesto. Este puerto no sale de la red interna
			// del compose.
			CheckOrigin:     func(*http.Request) bool { return true },
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		},
	}
}

// Routes devuelve el mux con el WebSocket y un health para el compose.
func (h *WSHandler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/ws/books/", h.serveBook)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

func (h *WSHandler) serveBook(w http.ResponseWriter, r *http.Request) {
	bookID := strings.TrimPrefix(r.URL.Path, "/v1/ws/books/")
	// Se corta en la primera barra para que /v1/ws/books/{id}/loquesea no pase
	// como si fuera el id.
	if i := strings.IndexByte(bookID, '/'); i >= 0 {
		bookID = bookID[:i]
	}
	if bookID == "" {
		http.Error(w, "book id is required", http.StatusBadRequest)
		return
	}

	// El id de la peticion llega como cabecera HTTP normal (la pone Envoy) y no
	// como metadata gRPC: esta ruta no pasa por el transcoder. Se mete en el
	// contexto para que salga en el log de aqui y viaje a los vecinos en las
	// llamadas de abajo.
	ctx := logx.WithRequestID(r.Context(), r.Header.Get(logx.RequestIDKey))

	// El token va en la query y no en un header porque la API de WebSocket del
	// navegador no deja poner cabeceras en el handshake. Es opcional: el stream
	// es publico y lo unico que aporta es rechazar a quien manda uno invalido en
	// vez de degradarlo a anonimo en silencio.
	if token := r.URL.Query().Get("token"); token != "" {
		if _, err := h.tokens.ValidateToken(ctx, token); err != nil {
			http.Error(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}
	}

	// Se comprueba ANTES de aceptar el upgrade: asi un libro que no existe o no
	// esta publicado responde un 404 HTTP normal, que el cliente entiende. Si se
	// hiciera despues, tendria que cerrar un WebSocket ya abierto con un codigo
	// de cierre, que es mucho mas dificil de diagnosticar desde el navegador.
	if err := h.books.Exists(ctx, bookID); err != nil {
		http.Error(w, "book not found", http.StatusNotFound)
		return
	}

	// El snapshot tambien se pide antes del upgrade, por lo mismo: si la base no
	// responde, mejor un 503 que una conexion abierta que nunca dice nada.
	snapshot, err := h.snapshots.Snapshot(ctx, bookID)
	if err != nil {
		http.Error(w, "cannot load the current state", http.StatusServiceUnavailable)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade ya respondio al cliente; aqui solo queda dejar rastro.
		logx.From(ctx).Warn("ws upgrade failed",
			slog.String("book_id", bookID),
			slog.String("error", err.Error()))
		return
	}
	defer conn.Close()

	// La suscripcion se abre despues del upgrade pero el snapshot se leyo antes,
	// asi que hay una ventana en la que un cambio no entra en ninguno de los
	// dos. Se acepta: son milisegundos, y el precio de cerrarla —bloquear el
	// reparto mientras se arma cada conexion— es peor que un contador que puede
	// quedar uno corto hasta el siguiente evento.
	stream, unsubscribe := h.hub.Subscribe(bookID)
	defer unsubscribe()

	if err := writeJSON(conn, snapshotEvent(bookID, snapshot)); err != nil {
		return
	}

	// El bombeo de lectura corre aparte: no espera mensajes utiles, pero sin
	// leer nunca no llegarian los pong ni se detectaria el cierre del cliente.
	closed := make(chan struct{})
	go h.readPump(conn, closed)

	h.writePump(conn, stream, closed)
}

// readPump descarta lo que mande el cliente y refresca el plazo con cada pong.
// Termina cuando la conexion se cierra, y al cerrar closed le avisa al escritor.
func (h *WSHandler) readPump(conn *websocket.Conn, closed chan struct{}) {
	defer close(closed)

	conn.SetReadLimit(maxClientMessage)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

// writePump manda los eventos y los pings. Es el unico que escribe en la
// conexion: gorilla no admite dos escritores a la vez, y tener uno solo es mas
// simple que coordinarlos con un mutex.
func (h *WSHandler) writePump(conn *websocket.Conn, stream <-chan events.Event, closed <-chan struct{}) {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	for {
		select {
		case evt, ok := <-stream:
			if !ok {
				return
			}
			if err := writeJSON(conn, evt); err != nil {
				return
			}
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-closed:
			return
		}
	}
}

func writeJSON(conn *websocket.Conn, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	// El plazo de escritura evita que un cliente que dejo de leer deje el envio
	// colgado indefinidamente ocupando la goroutine.
	if err := conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func snapshotEvent(bookID string, s *domain.BookInteractions) events.Event {
	count := s.CommentCount
	return events.Event{
		Type:         events.TypeSnapshot,
		BookID:       bookID,
		Likes:        &events.LikePayload{Count: s.Likes.Count},
		Rating:       &events.RatingPayload{Average: s.Rating.Average, Count: s.Rating.Count},
		CommentCount: &count,
	}
}
