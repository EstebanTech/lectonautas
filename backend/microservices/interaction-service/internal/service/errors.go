package service

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/repository"
	"github.com/EstebanTech/lectonautas/backend/shared/logx"
)

// mapBookErr deja pasar tal cual lo que ya viene como error gRPC del cliente de
// library-service (el NotFound del libro), y convierte cualquier otra cosa en
// Unavailable: si el vecino no responde, no es que el libro no exista, es que
// ahora mismo no se puede saber. Decir NotFound ahi haria que el cliente borrara
// de su pantalla un libro que sigue estando.
func mapBookErr(ctx context.Context, err error) error {
	if _, ok := status.FromError(err); ok && status.Code(err) != codes.Unknown {
		return err
	}
	logx.From(ctx).Error("library-service call failed", slog.String("error", err.Error()))
	return status.Error(codes.Unavailable, "cannot verify the book right now")
}

// mapRepoErr traduce los errores del repositorio a codigos gRPC. El fallback
// deja el detalle en el log y le devuelve al cliente un mensaje generico.
//
// Lleva ctx solo para el log: es lo que le pone el id de la peticion a la
// linea, y un error sin ese id es justo el que no se puede seguir hasta el
// servicio que lo origino.
func mapRepoErr(ctx context.Context, err error, fallback string) error {
	switch {
	case errors.Is(err, repository.ErrCommentNotFound):
		return status.Error(codes.NotFound, "comment not found")
	case errors.Is(err, repository.ErrRatingNotFound):
		return status.Error(codes.NotFound, "you have not rated this book")
	case errors.Is(err, repository.ErrBookNotFound):
		return status.Error(codes.NotFound, "book not found")
	default:
		logx.From(ctx).Error("repository error", slog.String("error", err.Error()))
		return status.Error(codes.Internal, fallback)
	}
}
