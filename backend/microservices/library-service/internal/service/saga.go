package service

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/cache"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/domain"
	libraryv1 "github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/proto/library/v1"
)

// cachedSagaDetail es lo que se guarda en Valkey para GetSaga.
type cachedSagaDetail struct {
	Saga  *domain.Saga   `json:"saga"`
	Books []*domain.Book `json:"books"`
}

func (s *LibraryService) CreateSaga(ctx context.Context, req *libraryv1.CreateSagaRequest) (*libraryv1.SagaResponse, error) {
	authorID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}

	title, err := requiredText("title", req.GetTitle(), titleMaxLen)
	if err != nil {
		return nil, err
	}
	description, err := optionalText("description", req.GetDescription(), descriptionMaxLen)
	if err != nil {
		return nil, err
	}

	created, err := s.sagas.Create(ctx, &domain.Saga{
		AuthorID:    authorID,
		Title:       title,
		Description: description,
	})
	if err != nil {
		return nil, mapRepoErr(err, "failed to create saga")
	}

	s.invalidate(ctx)

	return &libraryv1.SagaResponse{Saga: sagaToProto(created)}, nil
}

// cachedSagaList es lo que se guarda en Valkey para los listados de sagas.
type cachedSagaList struct {
	Sagas []*domain.Saga `json:"sagas"`
	Total int32          `json:"total"`
}

// ListSagas es el listado publico. Las sagas no tienen estado, asi que no hay
// borradores que ocultar: lo que se filtra son los libros que devuelve GetSaga.
func (s *LibraryService) ListSagas(ctx context.Context, req *libraryv1.ListSagasRequest) (*libraryv1.ListSagasResponse, error) {
	authorID, err := optionalUUIDFilter("author_id", req.GetAuthorId())
	if err != nil {
		return nil, err
	}

	filter := normalizeSagaPagination(domain.SagaFilter{
		Page:     req.GetPage(),
		PageSize: req.GetPageSize(),
		AuthorID: authorID,
		Search:   req.GetSearch(),
	})

	return s.listSagas(ctx, filter, "pub")
}

// ListMySagas devuelve las sagas del autor autenticado. El author_id sale del
// token, igual que en ListMyBooks.
func (s *LibraryService) ListMySagas(ctx context.Context, req *libraryv1.ListMySagasRequest) (*libraryv1.ListSagasResponse, error) {
	authorID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}

	filter := normalizeSagaPagination(domain.SagaFilter{
		Page:     req.GetPage(),
		PageSize: req.GetPageSize(),
		AuthorID: authorID,
		Search:   req.GetSearch(),
	})

	return s.listSagas(ctx, filter, "own")
}

func (s *LibraryService) listSagas(ctx context.Context, filter domain.SagaFilter, scope string) (*libraryv1.ListSagasResponse, error) {
	var cached cachedSagaList
	key, hit := s.cacheGet(ctx, &cached, "sagas",
		scope, filter.AuthorID, filter.Search,
		cache.FormatInt(filter.Page), cache.FormatInt(filter.PageSize))
	if hit {
		return &libraryv1.ListSagasResponse{
			Sagas:    sagasToProto(cached.Sagas),
			Total:    cached.Total,
			Page:     filter.Page,
			PageSize: filter.PageSize,
		}, nil
	}

	sagas, total, err := s.sagas.List(ctx, filter)
	if err != nil {
		return nil, mapRepoErr(err, "failed to list sagas")
	}

	s.cacheSet(ctx, key, cachedSagaList{Sagas: sagas, Total: total})

	return &libraryv1.ListSagasResponse{
		Sagas:    sagasToProto(sagas),
		Total:    total,
		Page:     filter.Page,
		PageSize: filter.PageSize,
	}, nil
}

func normalizeSagaPagination(f domain.SagaFilter) domain.SagaFilter {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 {
		f.PageSize = defaultPageSize
	}
	if f.PageSize > maxPageSize {
		f.PageSize = maxPageSize
	}
	return f
}

// GetSaga devuelve la saga con sus libros ordenados. La saga en si es publica
// (no tiene estado), pero sus libros se filtran igual que en cualquier otra
// lectura: un lector ajeno solo ve los publicados.
func (s *LibraryService) GetSaga(ctx context.Context, req *libraryv1.GetSagaRequest) (*libraryv1.SagaDetailResponse, error) {
	id, err := requiredID("id", req.GetId())
	if err != nil {
		return nil, err
	}

	callerID, err := s.auth.Optional(ctx)
	if err != nil {
		return nil, err
	}

	saga, err := s.sagas.GetByID(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err, "failed to load saga")
	}
	isAuthor := callerID != "" && saga.AuthorID == callerID

	scope := "pub"
	if isAuthor {
		scope = "own"
	}
	var cached cachedSagaDetail
	key, hit := s.cacheGet(ctx, &cached, "saga", id, scope)
	if hit {
		return &libraryv1.SagaDetailResponse{
			Saga:  sagaToProto(cached.Saga),
			Books: booksToProto(cached.Books),
		}, nil
	}

	books, err := s.sagas.ListBooks(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err, "failed to load saga books")
	}
	if !isAuthor {
		visible := make([]*domain.Book, 0, len(books))
		for _, b := range books {
			if b.Status == domain.BookStatusPublished {
				visible = append(visible, b)
			}
		}
		books = visible
	}

	s.cacheSet(ctx, key, cachedSagaDetail{Saga: saga, Books: books})

	return &libraryv1.SagaDetailResponse{
		Saga:  sagaToProto(saga),
		Books: booksToProto(books),
	}, nil
}

func (s *LibraryService) UpdateSaga(ctx context.Context, req *libraryv1.UpdateSagaRequest) (*libraryv1.SagaResponse, error) {
	id, err := requiredID("id", req.GetId())
	if err != nil {
		return nil, err
	}

	callerID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.ownedSaga(ctx, id, callerID); err != nil {
		return nil, err
	}

	title, err := validateRequiredUpdate("title", req.Title, titleMaxLen)
	if err != nil {
		return nil, err
	}
	description, err := validateOptionalText("description", req.Description, descriptionMaxLen)
	if err != nil {
		return nil, err
	}

	updated, err := s.sagas.Update(ctx, &domain.SagaUpdate{
		ID:          id,
		Title:       title,
		Description: description,
	})
	if err != nil {
		return nil, mapRepoErr(err, "failed to update saga")
	}

	s.invalidate(ctx)

	return &libraryv1.SagaResponse{Saga: sagaToProto(updated)}, nil
}

// DeleteSaga borra la saga y sus vinculos; los libros no se tocan, porque
// pertenecer a una saga es opcional.
func (s *LibraryService) DeleteSaga(ctx context.Context, req *libraryv1.DeleteSagaRequest) (*libraryv1.DeleteResponse, error) {
	id, err := requiredID("id", req.GetId())
	if err != nil {
		return nil, err
	}

	callerID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.ownedSaga(ctx, id, callerID); err != nil {
		return nil, err
	}

	if err := s.sagas.Delete(ctx, id); err != nil {
		return nil, mapRepoErr(err, "failed to delete saga")
	}

	s.invalidate(ctx)

	return &libraryv1.DeleteResponse{Success: true}, nil
}

// AddBookToSaga vincula un libro a la saga. Exige que ambos sean del mismo
// autor: una saga es una coleccion de la obra propia, no una lista de lecturas
// (para eso esta el modulo reader).
func (s *LibraryService) AddBookToSaga(ctx context.Context, req *libraryv1.AddBookToSagaRequest) (*libraryv1.SagaDetailResponse, error) {
	sagaID, err := requiredID("saga_id", req.GetSagaId())
	if err != nil {
		return nil, err
	}
	bookID, err := requiredID("book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}

	callerID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}
	saga, err := s.ownedSaga(ctx, sagaID, callerID)
	if err != nil {
		return nil, err
	}
	if _, err := s.ownedBook(ctx, bookID, callerID); err != nil {
		return nil, err
	}

	if req.GetPosition() < 0 {
		return nil, status.Error(codes.InvalidArgument, "position must be greater than zero")
	}

	if err := s.sagas.AddBook(ctx, sagaID, bookID, req.GetPosition()); err != nil {
		return nil, mapRepoErr(err, "failed to add book to saga")
	}

	s.invalidate(ctx)

	books, err := s.sagas.ListBooks(ctx, sagaID)
	if err != nil {
		return nil, mapRepoErr(err, "failed to load saga books")
	}

	return &libraryv1.SagaDetailResponse{
		Saga:  sagaToProto(saga),
		Books: booksToProto(books),
	}, nil
}

func (s *LibraryService) RemoveBookFromSaga(ctx context.Context, req *libraryv1.RemoveBookFromSagaRequest) (*libraryv1.DeleteResponse, error) {
	sagaID, err := requiredID("saga_id", req.GetSagaId())
	if err != nil {
		return nil, err
	}
	bookID, err := requiredID("book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}

	callerID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.ownedSaga(ctx, sagaID, callerID); err != nil {
		return nil, err
	}

	if err := s.sagas.RemoveBook(ctx, sagaID, bookID); err != nil {
		return nil, mapRepoErr(err, "failed to remove book from saga")
	}

	s.invalidate(ctx)

	return &libraryv1.DeleteResponse{Success: true}, nil
}

// ReorderSagaBooks recibe todos los libros de la saga en el orden deseado.
func (s *LibraryService) ReorderSagaBooks(ctx context.Context, req *libraryv1.ReorderSagaBooksRequest) (*libraryv1.SagaDetailResponse, error) {
	sagaID, err := requiredID("saga_id", req.GetSagaId())
	if err != nil {
		return nil, err
	}

	callerID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}
	saga, err := s.ownedSaga(ctx, sagaID, callerID)
	if err != nil {
		return nil, err
	}

	ids := req.GetBookIds()
	if len(ids) == 0 {
		return nil, status.Error(codes.InvalidArgument, "book_ids is required")
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			return nil, status.Error(codes.InvalidArgument, "book_ids cannot contain empty values")
		}
		if _, dup := seen[id]; dup {
			return nil, status.Error(codes.InvalidArgument, "book_ids cannot contain duplicates")
		}
		seen[id] = struct{}{}
	}

	if err := s.sagas.ReorderBooks(ctx, sagaID, ids); err != nil {
		return nil, mapRepoErr(err, "failed to reorder saga books")
	}

	s.invalidate(ctx)

	books, err := s.sagas.ListBooks(ctx, sagaID)
	if err != nil {
		return nil, mapRepoErr(err, "failed to load saga books")
	}

	return &libraryv1.SagaDetailResponse{
		Saga:  sagaToProto(saga),
		Books: booksToProto(books),
	}, nil
}
