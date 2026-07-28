package service

import (
	"context"

	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/cache"
	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/domain"
	libraryv1 "github.com/EstebanTech/lectonautas/backend/microservices/library-service/proto/library/v1"
)

// cachedSagaDetail es lo que se guarda en Valkey para GetSaga.
type cachedSagaDetail struct {
	Saga  *domain.Saga   `json:"saga"`
	Books []*domain.Book `json:"books"`
}

// cachedSagaList es lo que se guarda en Valkey para los listados de sagas.
type cachedSagaList struct {
	Sagas []*domain.Saga `json:"sagas"`
	Total int32          `json:"total"`
}

func (s *LibraryService) CreateSaga(ctx context.Context, req *libraryv1.CreateSagaRequest) (*libraryv1.SagaResponse, error) {
	authorID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}

	title, err := titleField.Required(req.GetTitle())
	if err != nil {
		return nil, err
	}
	description, err := descriptionField.Optional(req.GetDescription())
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

	s.cache.Invalidate(ctx)

	return &libraryv1.SagaResponse{Saga: sagaToProto(created)}, nil
}

// ListSagas es el listado publico. Las sagas no tienen estado, asi que no hay
// borradores que ocultar: lo que se filtra son los libros que devuelve GetSaga.
func (s *LibraryService) ListSagas(ctx context.Context, req *libraryv1.ListSagasRequest) (*libraryv1.ListSagasResponse, error) {
	authorID, err := optionalUUIDFilter("author_id", req.GetAuthorId())
	if err != nil {
		return nil, err
	}

	filter := domain.SagaFilter{
		AuthorID: authorID,
		Search:   req.GetSearch(),
	}
	filter.Page, filter.PageSize = normalizePage(req.GetPage(), req.GetPageSize())

	return s.listSagas(ctx, filter, scopePublic)
}

// ListMySagas devuelve las sagas del autor autenticado. El author_id sale del
// token, igual que en ListMyBooks.
func (s *LibraryService) ListMySagas(ctx context.Context, req *libraryv1.ListMySagasRequest) (*libraryv1.ListSagasResponse, error) {
	authorID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}

	filter := domain.SagaFilter{
		AuthorID: authorID,
		Search:   req.GetSearch(),
	}
	filter.Page, filter.PageSize = normalizePage(req.GetPage(), req.GetPageSize())

	return s.listSagas(ctx, filter, scopeOwner)
}

func (s *LibraryService) listSagas(ctx context.Context, filter domain.SagaFilter, scope string) (*libraryv1.ListSagasResponse, error) {
	var cached cachedSagaList
	key, hit := s.cache.Get(ctx, &cached, "sagas",
		scope, filter.AuthorID, filter.Search,
		cache.FormatInt(filter.Page), cache.FormatInt(filter.PageSize))
	if hit {
		return sagaList(cached.Sagas, cached.Total, filter), nil
	}

	sagas, total, err := s.sagas.List(ctx, filter)
	if err != nil {
		return nil, mapRepoErr(err, "failed to list sagas")
	}

	s.cache.Set(ctx, key, cachedSagaList{Sagas: sagas, Total: total})

	return sagaList(sagas, total, filter), nil
}

func sagaList(sagas []*domain.Saga, total int32, filter domain.SagaFilter) *libraryv1.ListSagasResponse {
	return &libraryv1.ListSagasResponse{
		Sagas:    sagasToProto(sagas),
		Total:    total,
		Page:     filter.Page,
		PageSize: filter.PageSize,
	}
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

	// Mismo atajo que en GetBook: si la version publica esta cacheada y quien
	// pregunta no es el autor de la saga, ya esta servida sin tocar Postgres.
	var pub cachedSagaDetail
	pubKey, hit := s.cache.Get(ctx, &pub, "saga", id, scopePublic)
	if hit && pub.Saga != nil && pub.Saga.AuthorID != callerID {
		return sagaDetail(pub.Saga, pub.Books), nil
	}

	saga, err := s.sagas.GetByID(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err, "failed to load saga")
	}
	isAuthor := callerID != "" && saga.AuthorID == callerID

	key := pubKey
	if isAuthor {
		var own cachedSagaDetail
		ownKey, ownHit := s.cache.Get(ctx, &own, "saga", id, scopeOwner)
		if ownHit && own.Saga != nil {
			return sagaDetail(own.Saga, own.Books), nil
		}
		key = ownKey
	}

	books, err := s.sagas.ListBooks(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err, "failed to load saga books")
	}
	if !isAuthor {
		books = publishedOnly(books)
	}

	s.cache.Set(ctx, key, cachedSagaDetail{Saga: saga, Books: books})

	return sagaDetail(saga, books), nil
}

// publishedOnly deja fuera los libros que el lector ajeno no puede ver. Se
// filtra aqui y no en la consulta porque la misma lista, sin filtrar, es la que
// le toca al autor.
func publishedOnly(books []*domain.Book) []*domain.Book {
	visible := make([]*domain.Book, 0, len(books))
	for _, b := range books {
		if b.Status == domain.BookStatusPublished {
			visible = append(visible, b)
		}
	}
	return visible
}

func sagaDetail(saga *domain.Saga, books []*domain.Book) *libraryv1.SagaDetailResponse {
	return &libraryv1.SagaDetailResponse{
		Saga:  sagaToProto(saga),
		Books: booksToProto(books),
	}
}

func (s *LibraryService) UpdateSaga(ctx context.Context, req *libraryv1.UpdateSagaRequest) (*libraryv1.SagaResponse, error) {
	saga, _, err := s.requireOwnedSaga(ctx, "id", req.GetId())
	if err != nil {
		return nil, err
	}

	title, err := titleField.UpdateRequired(req.Title)
	if err != nil {
		return nil, err
	}
	description, err := descriptionField.Update(req.Description)
	if err != nil {
		return nil, err
	}

	updated, err := s.sagas.Update(ctx, &domain.SagaUpdate{
		ID:          saga.ID,
		Title:       title,
		Description: description,
	})
	if err != nil {
		return nil, mapRepoErr(err, "failed to update saga")
	}

	s.cache.Invalidate(ctx)

	return &libraryv1.SagaResponse{Saga: sagaToProto(updated)}, nil
}

// DeleteSaga borra la saga y sus vinculos; los libros no se tocan, porque
// pertenecer a una saga es opcional.
func (s *LibraryService) DeleteSaga(ctx context.Context, req *libraryv1.DeleteSagaRequest) (*libraryv1.DeleteResponse, error) {
	saga, _, err := s.requireOwnedSaga(ctx, "id", req.GetId())
	if err != nil {
		return nil, err
	}

	if err := s.sagas.Delete(ctx, saga.ID); err != nil {
		return nil, mapRepoErr(err, "failed to delete saga")
	}

	s.cache.Invalidate(ctx)

	return &libraryv1.DeleteResponse{Success: true}, nil
}

// AddBookToSaga vincula un libro a la saga. Exige que ambos sean del mismo
// autor: una saga es una coleccion de la obra propia, no una lista de lecturas
// (para eso esta el modulo reader).
func (s *LibraryService) AddBookToSaga(ctx context.Context, req *libraryv1.AddBookToSagaRequest) (*libraryv1.SagaDetailResponse, error) {
	saga, callerID, err := s.requireOwnedSaga(ctx, "saga_id", req.GetSagaId())
	if err != nil {
		return nil, err
	}
	bookID, err := requiredID("book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}
	if _, err := s.ownedBook(ctx, bookID, callerID); err != nil {
		return nil, err
	}

	if err := requiredPosition(req.GetPosition()); err != nil {
		return nil, err
	}

	if err := s.sagas.AddBook(ctx, saga.ID, bookID, req.GetPosition()); err != nil {
		return nil, mapRepoErr(err, "failed to add book to saga")
	}

	s.cache.Invalidate(ctx)

	books, err := s.sagas.ListBooks(ctx, saga.ID)
	if err != nil {
		return nil, mapRepoErr(err, "failed to load saga books")
	}

	return sagaDetail(saga, books), nil
}

func (s *LibraryService) RemoveBookFromSaga(ctx context.Context, req *libraryv1.RemoveBookFromSagaRequest) (*libraryv1.DeleteResponse, error) {
	saga, _, err := s.requireOwnedSaga(ctx, "saga_id", req.GetSagaId())
	if err != nil {
		return nil, err
	}
	bookID, err := requiredID("book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}

	if err := s.sagas.RemoveBook(ctx, saga.ID, bookID); err != nil {
		return nil, mapRepoErr(err, "failed to remove book from saga")
	}

	s.cache.Invalidate(ctx)

	return &libraryv1.DeleteResponse{Success: true}, nil
}

// ReorderSagaBooks recibe todos los libros de la saga en el orden deseado.
func (s *LibraryService) ReorderSagaBooks(ctx context.Context, req *libraryv1.ReorderSagaBooksRequest) (*libraryv1.SagaDetailResponse, error) {
	saga, _, err := s.requireOwnedSaga(ctx, "saga_id", req.GetSagaId())
	if err != nil {
		return nil, err
	}

	ids := req.GetBookIds()
	if err := requiredIDs("book_ids", ids); err != nil {
		return nil, err
	}

	if err := s.sagas.ReorderBooks(ctx, saga.ID, ids); err != nil {
		return nil, mapRepoErr(err, "failed to reorder saga books")
	}

	s.cache.Invalidate(ctx)

	books, err := s.sagas.ListBooks(ctx, saga.ID)
	if err != nil {
		return nil, mapRepoErr(err, "failed to load saga books")
	}

	return sagaDetail(saga, books), nil
}
