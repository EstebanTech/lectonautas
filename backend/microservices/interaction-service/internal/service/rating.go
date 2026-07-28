package service

import (
	"context"

	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/domain"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/events"
	interactionv1 "github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/proto/interaction/v1"
)

// RateBook deja la calificacion del llamante entre 1 y 5. Es PUT y no POST
// porque un lector tiene una sola nota por libro: volver a calificar cambia la
// suya, no agrega otro voto.
func (s *InteractionService) RateBook(ctx context.Context, req *interactionv1.RateBookRequest) (*interactionv1.RatingSummaryResponse, error) {
	bookID, err := requiredID("book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}

	userID, _, err := s.writable(ctx, bookID)
	if err != nil {
		return nil, err
	}

	score, err := validateScore(req.GetScore())
	if err != nil {
		return nil, err
	}

	summary, err := s.ratings.Rate(ctx, bookID, userID, score)
	if err != nil {
		return nil, mapRepoErr(err, "failed to rate book")
	}

	s.cache.Invalidate(ctx)
	s.publishRating(ctx, summary)

	return &interactionv1.RatingSummaryResponse{Rating: ratingSummaryToProto(summary)}, nil
}

// DeleteRating quita el voto del llamante. Devuelve el resumen ya sin el, en
// vez de un simple success, porque el cliente que quita su nota necesita el
// promedio nuevo para repintarlo.
func (s *InteractionService) DeleteRating(ctx context.Context, req *interactionv1.DeleteRatingRequest) (*interactionv1.RatingSummaryResponse, error) {
	bookID, err := requiredID("book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}

	userID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}

	// Sin pasar por writable: quitar el voto no es interactuar con el libro, es
	// deshacerlo. Si el autor lo despublico despues, el lector tiene que poder
	// retirar su nota igual.
	if err := s.ratings.Delete(ctx, bookID, userID); err != nil {
		return nil, mapRepoErr(err, "failed to delete rating")
	}

	summary, err := s.ratings.Summary(ctx, bookID, userID)
	if err != nil {
		return nil, mapRepoErr(err, "failed to load rating")
	}

	s.cache.Invalidate(ctx)
	s.publishRating(ctx, summary)

	return &interactionv1.RatingSummaryResponse{Rating: ratingSummaryToProto(summary)}, nil
}

// GetBookRating es publico. Con token, ademas dice el voto propio.
func (s *InteractionService) GetBookRating(ctx context.Context, req *interactionv1.GetBookRatingRequest) (*interactionv1.RatingSummaryResponse, error) {
	bookID, err := requiredID("book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}

	viewerID, err := s.auth.Optional(ctx)
	if err != nil {
		return nil, err
	}

	summary, err := s.ratingSummary(ctx, bookID, viewerID)
	if err != nil {
		return nil, err
	}

	return &interactionv1.RatingSummaryResponse{Rating: ratingSummaryToProto(summary)}, nil
}

// ratingSummary lee el resumen, con cache. Como en los me gusta, el viewer
// entra en la clave porque myScore es distinto para cada uno.
func (s *InteractionService) ratingSummary(ctx context.Context, bookID, viewerID string) (*domain.RatingSummary, error) {
	var cached domain.RatingSummary
	key, hit := s.cache.Get(ctx, &cached, "rating", bookID, viewerID)
	if hit {
		return &cached, nil
	}

	summary, err := s.ratings.Summary(ctx, bookID, viewerID)
	if err != nil {
		return nil, mapRepoErr(err, "failed to load rating")
	}

	s.cache.Set(ctx, key, summary)
	return summary, nil
}

// publishRating manda el promedio nuevo. No lleva myScore: el evento es uno
// solo para todos los que estan mirando el libro, y el voto propio es de cada
// quien.
func (s *InteractionService) publishRating(ctx context.Context, summary *domain.RatingSummary) {
	s.bus.Publish(ctx, events.Event{
		Type:   events.TypeRatingChanged,
		BookID: summary.BookID,
		Rating: &events.RatingPayload{Average: summary.Average, Count: summary.Count},
	})
}
