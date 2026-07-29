package grpcx

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/EstebanTech/lectonautas/backend/shared/logx"
)

// Dial abre la conexión a un servicio vecino.
//
// grpc.NewClient no conecta al vuelo: la conexión se establece en la primera
// llamada, así que arrancar antes que el vecino no es un problema y ninguna de
// estas dependencias tiene por qué entrar en el depends_on del compose.
//
// El interceptor añade a cada llamada saliente el id de la petición que la
// originó —para que el log del vecino se pueda juntar con el de aquí— y el
// secreto compartido, que es lo que le permite al otro lado aceptar sus
// métodos internos.
//
// Sin TLS: todo esto viaja por la red interna del compose y nada de ello sale
// al exterior. El día que los servicios no compartan red, esto es lo primero
// que hay que cambiar.
func Dial(addr, internalSecret string) (*grpc.ClientConn, error) {
	return grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(outgoingMetadataInterceptor(internalSecret)),
	)
}

func outgoingMetadataInterceptor(internalSecret string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if internalSecret != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, InternalTokenKey, internalSecret)
		}
		if id := logx.RequestID(ctx); id != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, logx.RequestIDKey, id)
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
