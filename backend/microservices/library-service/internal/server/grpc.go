package server

import (
	"google.golang.org/grpc"

	libraryv1 "github.com/EstebanTech/lectonautas/backend/microservices/library-service/proto/library/v1"
	"github.com/EstebanTech/lectonautas/backend/shared/grpcx"
)

// internalMethods son los que solo puede llamar otro servicio. Aqui hay uno
// solo: el borrado de toda la obra de un autor, que dispara user-service cuando
// se da de baja una cuenta. Desde fuera seria un borrado masivo con un user_id
// por unica credencial, y el user_id es publico.
//
// Estan tambien bloqueados en el gateway, pero eso solo cubre a quien entra por
// la puerta; esto cubre a cualquiera que ya este dentro de la red.
var internalMethods = []string{
	libraryv1.LibraryService_DeleteAuthorContent_FullMethodName,
}

func NewGRPCServer(libraryService libraryv1.LibraryServiceServer, internalSecret string) *grpc.Server {
	s := grpcx.NewServer(grpcx.ServerConfig{
		Service:         "library-service",
		InternalSecret:  internalSecret,
		InternalMethods: internalMethods,
	})

	libraryv1.RegisterLibraryServiceServer(s, libraryService)
	grpcx.RegisterHealth(s)

	return s
}
