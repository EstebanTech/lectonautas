package service

import (
	"context"
	"log"

	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/repository"
	interactionv1 "github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/proto/interaction/v1"
)

// Los dos borrados de abajo no piden token ni comprueban propiedad, a
// diferencia de todo lo demas en este servicio: quien llama es el servicio
// dueno del recurso, que ya autorizo la operacion de su lado. Lo que impide que
// los llame cualquiera es que no existen como endpoint REST y que el gateway
// corta su ruta gRPC (envoy.yaml).
//
// Los dos son idempotentes: sobre algo que ya no tiene nada devuelven ceros. Es
// lo que permite que el llamante reintente si su propio borrado fallo despues.

// DeleteUserInteractions borra lo que dejo un usuario que se da de baja. Lo
// llama user-service.
func (s *InteractionService) DeleteUserInteractions(ctx context.Context, req *interactionv1.DeleteUserInteractionsRequest) (*interactionv1.DeleteInteractionsResponse, error) {
	userID, err := requiredID("user_id", req.GetUserId())
	if err != nil {
		return nil, err
	}

	counts, err := s.cleanup.DeleteByUser(ctx, userID)
	if err != nil {
		return nil, mapRepoErr(err, "failed to delete user interactions")
	}

	return s.finishCleanup(ctx, counts), nil
}

// DeleteBookInteractions borra lo que colgaba de un libro borrado. Lo llama
// library-service: sin esto quedarian comentarios de un libro que ya no existe,
// que ningun CASCADE puede limpiar porque viven en otra base.
func (s *InteractionService) DeleteBookInteractions(ctx context.Context, req *interactionv1.DeleteBookInteractionsRequest) (*interactionv1.DeleteInteractionsResponse, error) {
	bookID, err := requiredID("book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}

	counts, err := s.cleanup.DeleteByBook(ctx, bookID)
	if err != nil {
		return nil, mapRepoErr(err, "failed to delete book interactions")
	}

	return s.finishCleanup(ctx, counts), nil
}

// finishCleanup invalida el cache y arma la respuesta. No publica evento en el
// bus: los que estuvieran mirando ese libro ya no tienen libro que mirar, y en
// la baja de cuenta el borrado toca tantos libros que avisar de cada uno seria
// una tormenta de eventos por una operacion que pasa una vez.
func (s *InteractionService) finishCleanup(ctx context.Context, counts repository.Counts) *interactionv1.DeleteInteractionsResponse {
	s.cache.Invalidate(ctx)
	log.Printf("cleanup: %d likes, %d comments, %d ratings", counts.Likes, counts.Comments, counts.Ratings)

	return &interactionv1.DeleteInteractionsResponse{
		LikesDeleted:    counts.Likes,
		CommentsDeleted: counts.Comments,
		RatingsDeleted:  counts.Ratings,
	}
}
