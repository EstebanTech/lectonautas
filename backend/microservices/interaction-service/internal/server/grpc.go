package server

import (
	"google.golang.org/grpc"

	interactionv1 "github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/proto/interaction/v1"
	"github.com/EstebanTech/lectonautas/backend/shared/grpcx"
)

// internalMethods son los que solo puede llamar otro servicio: los dos borrados
// masivos que disparan user-service (baja de cuenta) y library-service (borrado
// de libro). Desde fuera serian un borrado masivo con un solo id por credencial,
// y tanto el user_id como el book_id son publicos.
//
// Estan tambien bloqueados en el gateway, pero eso solo cubre a quien entra por
// la puerta; esto cubre a cualquiera que ya este dentro de la red.
var internalMethods = []string{
	interactionv1.InteractionService_DeleteUserInteractions_FullMethodName,
	interactionv1.InteractionService_DeleteBookInteractions_FullMethodName,
}

func NewGRPCServer(interactionService interactionv1.InteractionServiceServer, internalSecret string) *grpc.Server {
	s := grpcx.NewServer(grpcx.ServerConfig{
		Service:         "interaction-service",
		InternalSecret:  internalSecret,
		InternalMethods: internalMethods,
	})

	interactionv1.RegisterInteractionServiceServer(s, interactionService)
	grpcx.RegisterHealth(s)

	return s
}
