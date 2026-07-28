package service

import (
	"errors"
	"log"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/domain"
	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/repository"
)

// mapRepoErr traduce los errores del repositorio a codigos gRPC. El fallback
// deja el detalle en el log y le devuelve al cliente un mensaje generico.
func mapRepoErr(err error, fallback string) error {
	switch {
	case errors.Is(err, repository.ErrBookNotFound):
		return status.Error(codes.NotFound, "book not found")
	case errors.Is(err, repository.ErrChapterNotFound):
		return status.Error(codes.NotFound, "chapter not found")
	case errors.Is(err, repository.ErrSagaNotFound):
		return status.Error(codes.NotFound, "saga not found")
	// El genero lo elige el cliente de una lista cerrada que le da la propia
	// API (ListGenres), asi que mandar uno que no esta es un error suyo, no un
	// recurso ausente.
	case errors.Is(err, repository.ErrGenreNotFound):
		return status.Error(codes.InvalidArgument, "unknown genre; see GET /v1/genres for the valid ones")
	case errors.Is(err, repository.ErrTooManyGenres):
		return status.Errorf(codes.InvalidArgument, "a book can have at most %d genres", domain.GenreMaxPerBook)
	case errors.Is(err, repository.ErrBookAlreadyInSaga):
		return status.Error(codes.AlreadyExists, "book already belongs to this saga")
	case errors.Is(err, repository.ErrPositionTaken):
		return status.Error(codes.AlreadyExists, "another chapter already occupies that position")
	case errors.Is(err, repository.ErrReorderMismatch):
		return status.Error(codes.InvalidArgument, "the list must contain every item exactly once")
	default:
		log.Printf("repository error: %v", err)
		return status.Error(codes.Internal, fallback)
	}
}
