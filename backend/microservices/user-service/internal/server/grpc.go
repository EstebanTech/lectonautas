package server

import (
	"context"
	"log"
	"runtime/debug"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	userv1 "github.com/EstebanTech/lectonautas/backend/microservices/user-service/proto/user/v1"
)

func NewGRPCServer(userService userv1.UserServiceServer) *grpc.Server {
	s := grpc.NewServer(
		// recovery va primero para envolver al resto: si un handler hace panic,
		// lo atrapa antes de que tumbe el proceso.
		grpc.ChainUnaryInterceptor(recoveryInterceptor, loggingInterceptor),
	)

	userv1.RegisterUserServiceServer(s, userService)
	reflection.Register(s)

	return s
}

// recoveryInterceptor evita que un panic en un handler tire abajo todo el
// servidor: lo captura, deja el detalle con su stack en el log y responde un
// error gRPC normal (Internal) en vez de matar el proceso.
func recoveryInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("method=%s panic=%v\n%s", info.FullMethod, r, debug.Stack())
			err = status.Error(codes.Internal, "internal error")
		}
	}()
	return handler(ctx, req)
}

func loggingInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	resp, err := handler(ctx, req)
	if err != nil {
		log.Printf("method=%s error=%v", info.FullMethod, err)
		return resp, err
	}
	log.Printf("method=%s status=ok", info.FullMethod)
	return resp, err
}
