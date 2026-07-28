package service

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/cache"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/domain"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/events"
	interactionv1 "github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/proto/interaction/v1"
)

// cachedCommentList es lo que se guarda en Valkey para ListComments: la pagina
// y el total, que no se puede recalcular desde la pagina sola.
type cachedCommentList struct {
	Comments []*domain.Comment `json:"comments"`
	Total    int32             `json:"total"`
}

func (s *InteractionService) CreateComment(ctx context.Context, req *interactionv1.CreateCommentRequest) (*interactionv1.CommentResponse, error) {
	bookID, err := requiredID("book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}

	userID, _, err := s.writable(ctx, bookID)
	if err != nil {
		return nil, err
	}

	body, err := validateBody(req.GetBody())
	if err != nil {
		return nil, err
	}

	created, err := s.comments.Create(ctx, &domain.Comment{
		BookID: bookID,
		UserID: userID,
		Body:   body,
	})
	if err != nil {
		return nil, mapRepoErr(err, "failed to create comment")
	}

	s.cache.Invalidate(ctx)
	s.publishComment(ctx, events.TypeCommentCreated, bookID, created, "")

	return &interactionv1.CommentResponse{Comment: commentToProto(created)}, nil
}

// ListComments es publico y paginado: los comentarios de un libro publicado los
// puede leer cualquiera, con token o sin el.
//
// A diferencia de las escrituras, no se pregunta a library-service si el libro
// existe: si no existe, simplemente no tiene comentarios y la respuesta vacia
// ya lo dice. Ahorra una llamada por cada lectura, que es el camino caliente.
func (s *InteractionService) ListComments(ctx context.Context, req *interactionv1.ListCommentsRequest) (*interactionv1.ListCommentsResponse, error) {
	bookID, err := requiredID("book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}

	filter := normalizePagination(domain.CommentFilter{
		BookID:   bookID,
		Page:     req.GetPage(),
		PageSize: req.GetPageSize(),
	})

	var cached cachedCommentList
	key, hit := s.cache.Get(ctx, &cached, "comments", bookID,
		cache.FormatInt(filter.Page), cache.FormatInt(filter.PageSize))
	if hit {
		return commentsResponse(cached.Comments, cached.Total, filter), nil
	}

	comments, total, err := s.comments.ListByBook(ctx, filter)
	if err != nil {
		return nil, mapRepoErr(err, "failed to list comments")
	}

	s.cache.Set(ctx, key, cachedCommentList{Comments: comments, Total: total})

	return commentsResponse(comments, total, filter), nil
}

// UpdateComment solo lo puede hacer quien lo escribio. El autor del libro no
// entra aqui a proposito: moderar es poder borrar lo que sobra en tu obra, no
// poner palabras en boca de otro.
func (s *InteractionService) UpdateComment(ctx context.Context, req *interactionv1.UpdateCommentRequest) (*interactionv1.CommentResponse, error) {
	bookID, err := requiredID("book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}
	id, err := requiredID("id", req.GetId())
	if err != nil {
		return nil, err
	}

	callerID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}

	body, err := validateBody(req.GetBody())
	if err != nil {
		return nil, err
	}

	existing, err := s.comments.GetByID(ctx, bookID, id)
	if err != nil {
		return nil, mapRepoErr(err, "failed to load comment")
	}
	if existing.UserID != callerID {
		return nil, status.Error(codes.PermissionDenied, "only the author can edit this comment")
	}

	updated, err := s.comments.Update(ctx, bookID, id, body)
	if err != nil {
		return nil, mapRepoErr(err, "failed to update comment")
	}

	s.cache.Invalidate(ctx)
	s.publishComment(ctx, events.TypeCommentUpdated, bookID, updated, "")

	return &interactionv1.CommentResponse{Comment: commentToProto(updated)}, nil
}

// DeleteComment lo borra su autor o el autor del libro. Lo segundo es la
// moderacion: la obra es de alguien, y ese alguien tiene que poder sacar lo que
// no quiere debajo.
//
// Aqui si se consulta a library-service, porque hace falta saber de quien es el
// libro. Se pide sin exigir que siga publicado: si el autor lo despublico, sus
// comentarios tienen que poder limpiarse igual.
func (s *InteractionService) DeleteComment(ctx context.Context, req *interactionv1.DeleteCommentRequest) (*interactionv1.DeleteResponse, error) {
	bookID, err := requiredID("book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}
	id, err := requiredID("id", req.GetId())
	if err != nil {
		return nil, err
	}

	callerID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}

	existing, err := s.comments.GetByID(ctx, bookID, id)
	if err != nil {
		return nil, mapRepoErr(err, "failed to load comment")
	}

	if existing.UserID != callerID && !s.ownsBook(ctx, bookID, callerID) {
		return nil, status.Error(codes.PermissionDenied,
			"only the comment author or the book author can delete this comment")
	}

	if err := s.comments.Delete(ctx, bookID, id); err != nil {
		return nil, mapRepoErr(err, "failed to delete comment")
	}

	s.cache.Invalidate(ctx)
	s.publishComment(ctx, events.TypeCommentDeleted, bookID, nil, id)

	return &interactionv1.DeleteResponse{Success: true}, nil
}

// ownsBook dice si el llamante es el autor del libro. Un fallo al preguntar se
// trata como "no lo es": ante la duda no se concede un permiso, y el que si es
// dueno del comentario ya paso por la comprobacion anterior.
func (s *InteractionService) ownsBook(ctx context.Context, bookID, callerID string) bool {
	book, err := s.books.PublishedBook(ctx, bookID)
	if err != nil {
		return false
	}
	return book.AuthorID == callerID
}

// publishComment arma y manda el evento del WebSocket. El conteo va con el
// evento para que el cliente no tenga que volver a preguntarlo solo para mover
// un numero; si no se puede leer, el evento sale igual pero sin el.
//
// El comentario es nil en el borrado, donde ya no hay nada que mandar salvo su
// id; por eso el bookID va aparte y no se saca de el.
func (s *InteractionService) publishComment(ctx context.Context, kind, bookID string, c *domain.Comment, commentID string) {
	evt := events.Event{Type: kind, BookID: bookID, CommentID: commentID}
	if c != nil {
		evt.Comment = commentToPayload(c)
	}
	if n, err := s.comments.CountByBook(ctx, bookID); err == nil {
		evt.CommentCount = &n
	}

	s.bus.Publish(ctx, evt)
}

func commentsResponse(comments []*domain.Comment, total int32, f domain.CommentFilter) *interactionv1.ListCommentsResponse {
	return &interactionv1.ListCommentsResponse{
		Comments: commentsToProto(comments),
		Total:    total,
		Page:     f.Page,
		PageSize: f.PageSize,
	}
}
