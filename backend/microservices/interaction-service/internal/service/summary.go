package service

import (
	"context"

	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/domain"
	interactionv1 "github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/proto/interaction/v1"
)

// GetBookInteractions devuelve me gusta, calificacion y numero de comentarios
// de una sola vez. Existe para que la ficha de un libro no tenga que hacer tres
// llamadas para pintar tres numeros que van juntos.
//
// Reutiliza las mismas lecturas cacheadas que los endpoints sueltos, asi que
// cuando el cliente pide primero el resumen y despues abre los comentarios, lo
// que ya estaba en Valkey no se vuelve a consultar.
func (s *InteractionService) GetBookInteractions(ctx context.Context, req *interactionv1.GetBookInteractionsRequest) (*interactionv1.BookInteractionsResponse, error) {
	bookID, err := requiredID("book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}

	viewerID, err := s.auth.Optional(ctx)
	if err != nil {
		return nil, err
	}

	likes, err := s.likeSummary(ctx, bookID, viewerID)
	if err != nil {
		return nil, err
	}
	rating, err := s.ratingSummary(ctx, bookID, viewerID)
	if err != nil {
		return nil, err
	}
	comments, err := s.commentCount(ctx, bookID)
	if err != nil {
		return nil, err
	}

	return &interactionv1.BookInteractionsResponse{
		Likes:        likeSummaryToProto(likes),
		Rating:       ratingSummaryToProto(rating),
		CommentCount: comments,
	}, nil
}

// commentCount es el unico dato del resumen que no depende de quien pregunta,
// asi que su clave de cache no lleva viewer y la comparten todos.
func (s *InteractionService) commentCount(ctx context.Context, bookID string) (int32, error) {
	var cached int32
	key, hit := s.cache.Get(ctx, &cached, "comment-count", bookID)
	if hit {
		return cached, nil
	}

	n, err := s.comments.CountByBook(ctx, bookID)
	if err != nil {
		return 0, mapRepoErr(err, "failed to count comments")
	}

	s.cache.Set(ctx, key, n)
	return n, nil
}

// Snapshot es lo que el WebSocket manda nada mas conectar, para que el cliente
// arranque con los numeros de ahora y no con la pantalla en blanco esperando
// que alguien haga algo. De ahi en adelante solo llegan cambios.
//
// Vive aqui y no en el servidor para que el WebSocket lea por el mismo camino
// (y el mismo cache) que el REST, en vez de tener su propia consulta.
func (s *InteractionService) Snapshot(ctx context.Context, bookID string) (*domain.BookInteractions, error) {
	// Sin viewer: el snapshot es el estado publico. Lo propio —si yo di me
	// gusta, que nota puse— lo trae el REST, que si sabe quien pregunta.
	likes, err := s.likeSummary(ctx, bookID, "")
	if err != nil {
		return nil, err
	}
	rating, err := s.ratingSummary(ctx, bookID, "")
	if err != nil {
		return nil, err
	}
	comments, err := s.commentCount(ctx, bookID)
	if err != nil {
		return nil, err
	}

	return &domain.BookInteractions{Likes: likes, Rating: rating, CommentCount: comments}, nil
}
