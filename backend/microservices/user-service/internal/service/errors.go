package service

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/repository"
)

// mapRepoErr traduce los errores del repositorio a codigos gRPC. El fallback
// le devuelve al cliente un mensaje generico.
func mapRepoErr(err error, fallback string) error {
	switch {
	case errors.Is(err, repository.ErrUserNotFound):
		return status.Error(codes.NotFound, "user not found")
	case errors.Is(err, repository.ErrEmailTaken):
		return status.Error(codes.AlreadyExists, "email already registered")
	case errors.Is(err, repository.ErrUsernameTaken):
		return status.Error(codes.AlreadyExists, "username already taken")
	default:
		return status.Error(codes.Internal, fallback)
	}
}
