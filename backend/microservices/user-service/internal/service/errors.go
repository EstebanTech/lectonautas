package service

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/repository"
	"github.com/EstebanTech/lectonautas/backend/shared/logx"
)

// mapRepoErr traduce los errores del repositorio a codigos gRPC. El fallback
// deja el detalle en el log y le devuelve al cliente un mensaje generico.
//
// Lleva ctx solo para el log: es lo que le pone el id de la peticion a la
// linea, y un error sin ese id es justo el que no se puede seguir hasta el
// servicio que lo origino.
func mapRepoErr(ctx context.Context, err error, fallback string) error {
	switch {
	case errors.Is(err, repository.ErrUserNotFound):
		return status.Error(codes.NotFound, "user not found")
	case errors.Is(err, repository.ErrEmailTaken):
		return status.Error(codes.AlreadyExists, "email already registered")
	case errors.Is(err, repository.ErrUsernameTaken):
		return status.Error(codes.AlreadyExists, "username already taken")
	default:
		logx.From(ctx).Error("repository error", slog.String("error", err.Error()))
		return status.Error(codes.Internal, fallback)
	}
}
