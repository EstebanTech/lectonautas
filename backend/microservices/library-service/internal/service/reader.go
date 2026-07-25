package service

import (
	"context"

	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/domain"
	libraryv1 "github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/proto/library/v1"
)

// El modulo reader siempre opera sobre la biblioteca del llamante: el user_id
// sale del token, nunca del cuerpo de la peticion.

// SaveBook guarda un libro como favorito o para leer mas tarde. Solo se puede
// guardar lo que el lector puede ver, asi que visibleBook decide: un borrador
// ajeno responde NotFound igual que en cualquier lectura.
func (s *LibraryService) SaveBook(ctx context.Context, req *libraryv1.SaveBookRequest) (*libraryv1.SavedBookResponse, error) {
	bookID, err := requiredID("book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}

	userID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}

	kind, err := validateSavedKind(req.GetKind())
	if err != nil {
		return nil, err
	}

	if _, _, err := s.visibleBook(ctx, bookID, userID); err != nil {
		return nil, err
	}

	saved, err := s.saved.Save(ctx, userID, bookID, kind)
	if err != nil {
		return nil, mapRepoErr(err, "failed to save book")
	}

	s.invalidate(ctx)

	return &libraryv1.SavedBookResponse{SavedBook: savedBookToProto(saved)}, nil
}

// ListSavedBooks devuelve la biblioteca del llamante. Sin kind trae las dos
// categorias juntas.
func (s *LibraryService) ListSavedBooks(ctx context.Context, req *libraryv1.ListSavedBooksRequest) (*libraryv1.ListSavedBooksResponse, error) {
	userID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}

	kind := ""
	if req.GetKind() != "" {
		if kind, err = validateSavedKind(req.GetKind()); err != nil {
			return nil, err
		}
	}

	var cached []*domain.SavedBook
	key, hit := s.cacheGet(ctx, &cached, "library", userID, kind)
	if hit {
		return savedBooksResponse(cached), nil
	}

	saved, err := s.saved.ListByUser(ctx, userID, kind)
	if err != nil {
		return nil, mapRepoErr(err, "failed to list saved books")
	}

	s.cacheSet(ctx, key, saved)

	return savedBooksResponse(saved), nil
}

// UnsaveBook quita el libro de la biblioteca. Sin kind lo saca de ambas
// categorias.
func (s *LibraryService) UnsaveBook(ctx context.Context, req *libraryv1.UnsaveBookRequest) (*libraryv1.DeleteResponse, error) {
	bookID, err := requiredID("book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}

	userID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}

	kind := ""
	if req.GetKind() != "" {
		if kind, err = validateSavedKind(req.GetKind()); err != nil {
			return nil, err
		}
	}

	if err := s.saved.Unsave(ctx, userID, bookID, kind); err != nil {
		return nil, mapRepoErr(err, "failed to remove saved book")
	}

	s.invalidate(ctx)

	return &libraryv1.DeleteResponse{Success: true}, nil
}

func savedBooksResponse(saved []*domain.SavedBook) *libraryv1.ListSavedBooksResponse {
	out := make([]*libraryv1.SavedBook, 0, len(saved))
	for _, sb := range saved {
		out = append(out, savedBookToProto(sb))
	}
	return &libraryv1.ListSavedBooksResponse{SavedBooks: out, Total: int32(len(out))}
}
