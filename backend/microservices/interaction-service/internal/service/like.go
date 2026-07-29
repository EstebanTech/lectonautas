package service

import (
	"context"

	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/domain"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/events"
	interactionv1 "github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/proto/interaction/v1"
)

// LikeBook pone el me gusta del llamante. Es idempotente: darlo dos veces deja
// el mismo estado y devuelve el mismo conteo, sin error. Se eligio asi porque
// el cliente tipico es un boton, y un doble clic o un reintento tras un
// timeout no son un caso de error, son la misma intencion repetida.
func (s *InteractionService) LikeBook(ctx context.Context, req *interactionv1.LikeBookRequest) (*interactionv1.LikeSummaryResponse, error) {
	return s.setLike(ctx, req.GetBookId(), true)
}

// UnlikeBook lo quita, con la misma regla de idempotencia.
func (s *InteractionService) UnlikeBook(ctx context.Context, req *interactionv1.UnlikeBookRequest) (*interactionv1.LikeSummaryResponse, error) {
	return s.setLike(ctx, req.GetBookId(), false)
}

// setLike es el cuerpo comun: las dos operaciones son la misma salvo el estado
// final y la fila que tocan.
func (s *InteractionService) setLike(ctx context.Context, rawBookID string, liked bool) (*interactionv1.LikeSummaryResponse, error) {
	bookID, err := requiredID("book_id", rawBookID)
	if err != nil {
		return nil, err
	}

	userID, _, err := s.writable(ctx, bookID)
	if err != nil {
		return nil, err
	}

	var summary *domain.LikeSummary
	if liked {
		summary, err = s.likes.Like(ctx, bookID, userID)
	} else {
		summary, err = s.likes.Unlike(ctx, bookID, userID)
	}
	if err != nil {
		return nil, mapRepoErr(ctx, err, "failed to update like")
	}

	s.cache.Invalidate(ctx)
	s.bus.Publish(ctx, events.Event{
		Type:   events.TypeLikeChanged,
		BookID: bookID,
		Likes:  &events.LikePayload{Count: summary.Count},
	})

	return &interactionv1.LikeSummaryResponse{Likes: likeSummaryToProto(summary)}, nil
}

// GetBookLikes es publico: el conteo de me gusta de un libro publicado no es
// secreto. El token es opcional y solo cambia likedByMe, no el conteo.
func (s *InteractionService) GetBookLikes(ctx context.Context, req *interactionv1.GetBookLikesRequest) (*interactionv1.LikeSummaryResponse, error) {
	bookID, err := requiredID("book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}

	viewerID, err := s.auth.Optional(ctx)
	if err != nil {
		return nil, err
	}

	summary, err := s.likeSummary(ctx, bookID, viewerID)
	if err != nil {
		return nil, err
	}

	return &interactionv1.LikeSummaryResponse{Likes: likeSummaryToProto(summary)}, nil
}

// likeSummary lee el resumen, con cache. El viewer entra en la clave porque
// likedByMe es distinto para cada uno: sin eso, el primero en preguntar le
// dejaria su propio estado cacheado a todos los demas.
func (s *InteractionService) likeSummary(ctx context.Context, bookID, viewerID string) (*domain.LikeSummary, error) {
	var cached domain.LikeSummary
	key, hit := s.cache.Get(ctx, &cached, "likes", bookID, viewerID)
	if hit {
		return &cached, nil
	}

	summary, err := s.likes.Summary(ctx, bookID, viewerID)
	if err != nil {
		return nil, mapRepoErr(ctx, err, "failed to load likes")
	}

	s.cache.Set(ctx, key, summary)
	return summary, nil
}
