package service

import (
	"context"

	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/domain"
	libraryv1 "github.com/EstebanTech/lectonautas/backend/microservices/library-service/proto/library/v1"
)

// ListGenres devuelve el catalogo completo. Es publico y no lleva paginacion:
// son menos de veinte filas y el cliente las quiere todas para armar el
// selector de generos de un libro.
func (s *LibraryService) ListGenres(ctx context.Context, _ *libraryv1.ListGenresRequest) (*libraryv1.ListGenresResponse, error) {
	var cached []*domain.Genre
	key, hit := s.cache.Get(ctx, &cached, "genres")
	if hit {
		return genresResponse(cached), nil
	}

	// El catalogo solo cambia con una migracion, asi que ninguna escritura de la
	// API lo invalida: lo unico que lo refresca es el TTL. Con un despliegue que
	// agregue generos, el selector tarda como mucho ese TTL en verlos.
	genres, err := s.genres.List(ctx)
	if err != nil {
		return nil, mapRepoErr(err, "failed to list genres")
	}

	s.cache.Set(ctx, key, genres)

	return genresResponse(genres), nil
}

// SetBookGenres reemplaza los generos del libro por los que lleguen. No es un
// PATCH: la lista que se manda es la que queda, y una lista vacia deja el libro
// sin generos.
func (s *LibraryService) SetBookGenres(ctx context.Context, req *libraryv1.SetBookGenresRequest) (*libraryv1.BookResponse, error) {
	book, _, err := s.requireOwnedBook(ctx, "book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}

	genres, err := validateGenres(req.GetGenres())
	if err != nil {
		return nil, err
	}

	if err := s.genres.ReplaceForBook(ctx, book.ID, genres); err != nil {
		return nil, mapRepoErr(err, "failed to set book genres")
	}

	s.cache.Invalidate(ctx)

	// Se relee para devolver el libro con los generos ya resueltos a su nombre:
	// la escritura solo conoce los slugs.
	updated, err := s.books.GetByID(ctx, book.ID)
	if err != nil {
		return nil, mapRepoErr(err, "failed to load book")
	}

	return &libraryv1.BookResponse{Book: bookToProto(updated)}, nil
}

func genresResponse(genres []*domain.Genre) *libraryv1.ListGenresResponse {
	out := genresToProto(genres)
	return &libraryv1.ListGenresResponse{Genres: out, Total: int32(len(out))}
}
