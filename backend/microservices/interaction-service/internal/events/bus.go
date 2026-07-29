// Package events es el canal por el que una escritura llega a los WebSocket
// abiertos.
//
// El problema que resuelve: quien escribe y quien esta escuchando no tienen por
// que estar en la misma replica. Si un lector comenta contra la instancia A y
// otro tiene el WebSocket abierto contra la B, la B no se entera de nada por su
// cuenta. Por eso el evento no se entrega en memoria sino que se publica en
// Valkey, al que estan suscritas TODAS las instancias, incluida la que publico
// — asi el camino es uno solo y no hay dos formas de entregar lo mismo.
//
// Es best-effort y a proposito: si Valkey se cae, se pierden avisos, no datos.
// La fuente de verdad sigue siendo Postgres y el cliente que recargue por REST
// ve el estado correcto. Un evento perdido no puede hacer fallar la escritura
// que ya se guardo.
package events

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/redis/go-redis/v9"

	"github.com/EstebanTech/lectonautas/backend/shared/logx"
)

// channel es uno solo para todos los libros, no uno por libro. Cada instancia
// recibe entonces eventos de libros que no esta mirando y los descarta, que es
// barato; a cambio no hay que suscribirse y desuscribirse a medida que entran y
// salen lectores, que es donde estarian las carreras.
const channel = "inter:events"

// Tipos de evento. Van al cliente tal cual, asi que son parte del contrato del
// WebSocket: cambiarlos rompe al que este escuchando.
const (
	TypeSnapshot       = "snapshot"
	TypeLikeChanged    = "like_changed"
	TypeRatingChanged  = "rating_changed"
	TypeCommentCreated = "comment_created"
	TypeCommentUpdated = "comment_updated"
	TypeCommentDeleted = "comment_deleted"
)

// Event es lo que viaja por el canal y lo que ve el cliente. Los punteros son
// para que un evento de "me gusta" no arrastre un rating en cero que el cliente
// interpretaria como un cambio: lo que no viene, no cambio.
type Event struct {
	Type   string `json:"type"`
	BookID string `json:"bookId"`

	Likes  *LikePayload   `json:"likes,omitempty"`
	Rating *RatingPayload `json:"rating,omitempty"`

	Comment *CommentPayload `json:"comment,omitempty"`
	// CommentID viaja solo en el borrado, donde ya no hay comentario que mandar.
	CommentID string `json:"commentId,omitempty"`
	// CommentCount va en todos los eventos de comentario, para que el contador
	// de la ficha no necesite otra consulta.
	CommentCount *int32 `json:"commentCount,omitempty"`
}

// LikePayload no lleva likedByMe: el conteo es igual para todos, pero "yo le di
// me gusta" depende de quien mira, y el evento se emite una vez para todos los
// suscriptores. Cada cliente ya sabe si fue el quien lo dio.
type LikePayload struct {
	Count int32 `json:"count"`
}

type RatingPayload struct {
	Average float64 `json:"average"`
	Count   int32   `json:"count"`
}

type CommentPayload struct {
	ID        string `json:"id"`
	BookID    string `json:"bookId"`
	UserID    string `json:"userId"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	Edited    bool   `json:"edited"`
}

// Publisher es lo que el servicio necesita del bus. Es una interfaz para poder
// probar las reglas de negocio sin Valkey y, de paso, afirmar que una escritura
// publica lo que debe.
type Publisher interface {
	Publish(ctx context.Context, evt Event)
}

// Bus publica y recibe eventos a traves de Valkey.
type Bus struct {
	rdb *redis.Client
}

func NewBus(rdb *redis.Client) *Bus {
	return &Bus{rdb: rdb}
}

// Publish manda el evento. No devuelve error a proposito: ningun llamante
// deberia abortar por esto, porque lo que se guardo en Postgres ya se guardo.
// El fallo queda en el log, que es lo unico que se puede hacer.
func (b *Bus) Publish(ctx context.Context, evt Event) {
	payload, err := json.Marshal(evt)
	if err != nil {
		logx.From(ctx).Error("event marshal failed", slog.String("error", err.Error()))
		return
	}
	if err := b.rdb.Publish(ctx, channel, payload).Err(); err != nil {
		logx.From(ctx).Error("event publish failed",
			slog.String("type", evt.Type),
			slog.String("error", err.Error()))
	}
}

// Subscribe devuelve un canal con los eventos de todas las instancias. Se cierra
// cuando ctx termina.
//
// go-redis reconecta solo si Valkey se cae y vuelve, asi que no hay que
// rearmar la suscripcion a mano; lo que pase mientras estuvo caido se pierde,
// que es el trato de un pub/sub sin persistencia.
func (b *Bus) Subscribe(ctx context.Context) <-chan Event {
	out := make(chan Event, 64)
	sub := b.rdb.Subscribe(ctx, channel)

	go func() {
		defer close(out)
		defer sub.Close()

		for msg := range sub.Channel() {
			var evt Event
			if err := json.Unmarshal([]byte(msg.Payload), &evt); err != nil {
				// Sin id de peticion: esto corre en la goroutine de la
				// suscripcion, que no pertenece a ninguna peticion concreta.
				slog.Error("event unmarshal failed", slog.String("error", err.Error()))
				continue
			}
			select {
			case out <- evt:
			case <-ctx.Done():
				return
			}
		}
	}()

	return out
}
