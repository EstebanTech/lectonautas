// Package grpcx arma el servidor y los clientes gRPC de los servicios: los
// interceptores que todos quieren igual (recuperación de panics, id de
// petición, log y autenticación entre servicios) y el health check.
package grpcx

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"runtime/debug"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	"github.com/EstebanTech/lectonautas/backend/shared/logx"
)

// InternalTokenKey es la metadata que llevan las llamadas de servicio a
// servicio. No la pone nunca un cliente de fuera: el gateway no la reenvía y,
// aunque lo hiciera, no conoce el secreto.
const InternalTokenKey = "x-internal-token"

// ServerConfig es lo que cambia de un servicio a otro.
type ServerConfig struct {
	// Service es el nombre que va en cada línea de log.
	Service string
	// InternalSecret autentica las llamadas entre servicios.
	InternalSecret string
	// InternalMethods son los métodos que SOLO puede llamar otro servicio, por
	// su nombre completo ("/library.v1.LibraryService/DeleteAuthorContent").
	// Están bloqueados en el gateway, pero eso solo cubre a quien entra por la
	// puerta: cualquier cosa dentro de la red podría llamarlos igual, y son
	// borrados masivos cuya única credencial sería un id que además es público.
	InternalMethods []string
}

// NewServer arma el servidor con la cadena de interceptores y registra el
// health check y la reflexión.
//
// El orden importa y es éste: recovery envuelve a todos, para que un panic en
// cualquier punto —incluidos los demás interceptores— no tumbe el proceso;
// requestID va justo después, porque todo lo que venga detrás quiere loguear
// con ese id; logging envuelve a internalAuth para que un intento de llamar un
// método interno sin credencial quede registrado, que es justo lo que hay que
// ver.
func NewServer(cfg ServerConfig, opts ...grpc.ServerOption) *grpc.Server {
	opts = append(opts, grpc.ChainUnaryInterceptor(
		recoveryInterceptor,
		requestIDInterceptor,
		loggingInterceptor,
		internalAuthInterceptor(cfg.InternalSecret, cfg.InternalMethods),
	))

	s := grpc.NewServer(opts...)
	reflection.Register(s)
	return s
}

// RegisterHealth registra el servicio de health check estándar de gRPC y lo
// deja en SERVING. Es lo que mira el healthcheck del compose: sin esto, lo
// único que se podía saber de un servicio es que su contenedor arrancó, que no
// es lo mismo que estar listo para responder.
func RegisterHealth(s *grpc.Server) *health.Server {
	hs := health.NewServer()
	grpc_health_v1.RegisterHealthServer(s, hs)
	// La cadena vacía es el servicio entero, que es lo que pregunta
	// grpc_health_probe cuando no se le da un nombre.
	hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	return hs
}

// recoveryInterceptor evita que un panic en un handler tire abajo todo el
// servidor: lo captura, deja el detalle con su stack en el log y responde un
// error gRPC normal (Internal) en vez de matar el proceso.
func recoveryInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
	defer func() {
		if r := recover(); r != nil {
			logx.From(ctx).Error("panic recovered",
				slog.String("method", info.FullMethod),
				slog.Any("panic", r),
				slog.String("stack", string(debug.Stack())),
			)
			err = status.Error(codes.Internal, "internal error")
		}
	}()
	return handler(ctx, req)
}

// requestIDInterceptor recoge el id de la petición o lo genera, lo deja en el
// contexto y lo devuelve en la respuesta. Que viaje de vuelta permite que quien
// reporta un fallo diga con qué id ocurrió, y que se pueda buscar directamente.
func requestIDInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	id := incomingRequestID(ctx)
	if id == "" {
		id = logx.NewRequestID()
	}
	if id != "" {
		ctx = logx.WithRequestID(ctx, id)
		// Un fallo aquí solo significa que el cliente no verá el id.
		_ = grpc.SetHeader(ctx, metadata.Pairs(logx.RequestIDKey, id))
	}
	return handler(ctx, req)
}

func incomingRequestID(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	values := md.Get(logx.RequestIDKey)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// loggingInterceptor deja una línea por petición con su resultado y cuánto
// tardó. El error va como campo aparte, no interpolado en el mensaje, para que
// se pueda filtrar por código sin buscar dentro de una cadena.
func loggingInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	start := time.Now()
	resp, err := handler(ctx, req)

	log := logx.From(ctx).With(
		slog.String("method", info.FullMethod),
		slog.Duration("took", time.Since(start)),
	)
	if err != nil {
		log.Error("request failed",
			slog.String("code", status.Code(err).String()),
			slog.String("error", err.Error()),
		)
		return resp, err
	}
	log.Info("request ok")
	return resp, nil
}

// internalAuthInterceptor exige el secreto compartido en los métodos que solo
// pueden llamar otros servicios.
//
// Responde NotFound y no PermissionDenied a propósito: es la misma respuesta
// que da el gateway para estos métodos, así que desde fuera no hay forma de
// distinguir "existe pero no puedes" de "no existe", y nadie aprende que hay un
// borrado masivo esperando la credencial correcta.
func internalAuthInterceptor(secret string, methods []string) grpc.UnaryServerInterceptor {
	internal := make(map[string]struct{}, len(methods))
	for _, m := range methods {
		internal[m] = struct{}{}
	}

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if _, ok := internal[info.FullMethod]; !ok {
			return handler(ctx, req)
		}

		if !validInternalToken(ctx, secret) {
			logx.From(ctx).Warn("internal method called without a valid token",
				slog.String("method", info.FullMethod))
			return nil, status.Error(codes.NotFound, "not found")
		}
		return handler(ctx, req)
	}
}

func validInternalToken(ctx context.Context, secret string) bool {
	if secret == "" {
		// Sin secreto configurado no hay nada con qué comparar. No se deja
		// pasar: fallar cerrado es lo único razonable cuando lo que protege es
		// un borrado masivo.
		return false
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return false
	}
	values := md.Get(InternalTokenKey)
	if len(values) == 0 {
		return false
	}
	// Comparación en tiempo constante: la diferencia de tiempo entre fallar en
	// el primer carácter y fallar en el último es explotable.
	return subtle.ConstantTimeCompare([]byte(values[0]), []byte(secret)) == 1
}
