// Package logx es el logging de los servicios: estructurado, con niveles y con
// el id de petición pegado a cada línea.
//
// El id es lo que hace que el log sirva con más de un servicio. Una petición
// real atraviesa varios —el lector comenta, interaction-service le pregunta a
// library-service si el libro existe, y ése le pregunta a user-service por el
// token—, y sin un hilo común el log de cada uno es una lista de eventos
// sueltos que no se pueden juntar. Envoy ya genera un `x-request-id` por
// petición; aquí solo se recoge, se propaga a los vecinos y se escribe.
package logx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"strings"
)

// RequestIDKey es el nombre con el que el id viaja entre servicios. Es el mismo
// header que pone Envoy, así que el id que ve el cliente en la respuesta es el
// que aparece en el log de los tres servicios.
const RequestIDKey = "x-request-id"

type ctxKey struct{}

// Setup deja configurado el logger global del proceso: JSON a stdout, con el
// nombre del servicio en cada línea. JSON y no texto porque el destino de esto
// es un colector que lo indexa, y "parsear" un log de texto es siempre una
// expresión regular esperando a romperse.
//
// El nivel sale de LOG_LEVEL (debug|info|warn|error), con info por defecto.
func Setup(service string) {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level()})
	slog.SetDefault(slog.New(handler).With(slog.String("service", service)))
}

func level() slog.Level {
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// WithRequestID guarda el id en el contexto, para que lo encuentren tanto el
// logger como el cliente gRPC que llame a un vecino.
func WithRequestID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, id)
}

// RequestID devuelve el id de la petición en curso, o cadena vacía fuera de una.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// From devuelve el logger con el id de la petición ya puesto. Es lo que hay que
// usar dentro de un handler; slog.Default() directo solo sirve en el arranque,
// donde todavía no hay petición a la que atribuir nada.
func From(ctx context.Context) *slog.Logger {
	if id := RequestID(ctx); id != "" {
		return slog.Default().With(slog.String("request_id", id))
	}
	return slog.Default()
}

// NewRequestID genera un id para las peticiones que llegan sin uno. Pasa cuando
// algo llama al servicio directo por gRPC, sin cruzar el gateway: los vecinos
// entre sí, o un grpcurl a mano.
func NewRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Sin aleatoriedad no hay id, pero tampoco hay motivo para tumbar la
		// petición: el log pierde correlación y nada más.
		return ""
	}
	return hex.EncodeToString(b[:])
}
