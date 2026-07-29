package server

import (
	"google.golang.org/grpc"

	userv1 "github.com/EstebanTech/lectonautas/backend/microservices/user-service/proto/user/v1"
	"github.com/EstebanTech/lectonautas/backend/shared/grpcx"
)

// internalMethods son los que solo puede llamar otro servicio. Aqui esta
// ValidateSession, que es como library-service e interaction-service resuelven
// el token de cada peticion: no tiene ruta HTTP, el gateway la corta, y ahora
// ademas exige el secreto compartido.
//
// Es la unica puerta a los datos de sesion, asi que dejarla abierta dentro de
// la red permitia a cualquiera que llegara ahi probar tokens contra ella.
var internalMethods = []string{
	userv1.UserService_ValidateSession_FullMethodName,
}

func NewGRPCServer(userService userv1.UserServiceServer, internalSecret string) *grpc.Server {
	s := grpcx.NewServer(grpcx.ServerConfig{
		Service:         "user-service",
		InternalSecret:  internalSecret,
		InternalMethods: internalMethods,
	})

	userv1.RegisterUserServiceServer(s, userService)
	grpcx.RegisterHealth(s)

	return s
}
