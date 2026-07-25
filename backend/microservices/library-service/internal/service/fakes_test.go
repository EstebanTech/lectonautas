package service

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/cache"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/domain"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/repository"
)

// Dobles en memoria de las dependencias del servicio. Solo implementan lo que
// las pruebas ejercitan; lo que no, falla explicitamente para que una prueba
// que dependa de algo no modelado no pase por accidente.

// UUIDs fijos para que las pruebas se lean sin ruido.
const (
	authorID    = "11111111-1111-1111-1111-111111111111"
	intruderID  = "22222222-2222-2222-2222-222222222222"
	bookID      = "33333333-3333-3333-3333-333333333333"
	chapterID   = "44444444-4444-4444-4444-444444444444"
	chapter2ID  = "55555555-5555-5555-5555-555555555555"
	sagaID      = "66666666-6666-6666-6666-666666666666"
	missingID   = "99999999-9999-9999-9999-999999999999"
	invalidUUID = "no-soy-un-uuid"
)

// --- Authenticator ----------------------------------------------------------

// fakeAuth simula las tres situaciones que importan: sin token, con token
// valido, y con token invalido o vencido.
type fakeAuth struct {
	userID  string // vacio = no vino token
	invalid bool   // vino un token, pero no sirve
}

func (f fakeAuth) Require(context.Context) (string, error) {
	if f.invalid {
		return "", status.Error(codes.Unauthenticated, "invalid or expired token")
	}
	if f.userID == "" {
		return "", status.Error(codes.Unauthenticated, "missing authorization header")
	}
	return f.userID, nil
}

func (f fakeAuth) Optional(context.Context) (string, error) {
	if f.invalid {
		return "", status.Error(codes.Unauthenticated, "invalid or expired token")
	}
	return f.userID, nil
}

// anonymous, asAuthor y asIntruder son los tres llamantes de las pruebas.
func anonymous() fakeAuth  { return fakeAuth{} }
func asAuthor() fakeAuth   { return fakeAuth{userID: authorID} }
func asIntruder() fakeAuth { return fakeAuth{userID: intruderID} }

// --- Cache ------------------------------------------------------------------

// noopCache no guarda nada: siempre es un miss. Las pruebas de reglas de
// negocio no deben depender de si algo estaba cacheado, y asi cada llamada
// ejercita el camino real contra el repositorio.
type noopCache struct{ bumps int }

func (c *noopCache) Key(_ context.Context, parts ...string) (string, error) {
	return strings.Join(parts, ":"), nil
}
func (c *noopCache) Get(context.Context, string, any) error { return cache.ErrMiss }
func (c *noopCache) Set(context.Context, string, any) error { return nil }
func (c *noopCache) Bump(context.Context) error             { c.bumps++; return nil }

// --- Repositorios -----------------------------------------------------------

type fakeBookRepo struct {
	books map[string]*domain.Book
}

func (r *fakeBookRepo) CreateWithFirstChapter(_ context.Context, b *domain.Book, ch *domain.Chapter) (*domain.Book, *domain.Chapter, error) {
	b.ID = bookID
	b.CreatedAt = time.Now()
	b.UpdatedAt = b.CreatedAt
	r.books[b.ID] = b

	ch.ID = chapterID
	ch.BookID = b.ID
	ch.Position = 1
	return b, ch, nil
}

func (r *fakeBookRepo) GetByID(_ context.Context, id string) (*domain.Book, error) {
	b, ok := r.books[id]
	if !ok {
		return nil, repository.ErrBookNotFound
	}
	return b, nil
}

func (r *fakeBookRepo) List(_ context.Context, f domain.BookFilter) ([]*domain.Book, int32, error) {
	out := make([]*domain.Book, 0)
	for _, b := range r.books {
		if f.AuthorID != "" && b.AuthorID != f.AuthorID {
			continue
		}
		if f.Status != "" && b.Status != f.Status {
			continue
		}
		out = append(out, b)
	}
	return out, int32(len(out)), nil
}

func (r *fakeBookRepo) Update(_ context.Context, upd *domain.BookUpdate) (*domain.Book, error) {
	b, ok := r.books[upd.ID]
	if !ok {
		return nil, repository.ErrBookNotFound
	}
	if upd.Title != nil {
		b.Title = *upd.Title
	}
	if upd.Status != nil {
		b.Status = *upd.Status
	}
	return b, nil
}

func (r *fakeBookRepo) Delete(_ context.Context, id string) error {
	if _, ok := r.books[id]; !ok {
		return repository.ErrBookNotFound
	}
	delete(r.books, id)
	return nil
}

type fakeChapterRepo struct {
	chapters map[string]*domain.Chapter
}

func (r *fakeChapterRepo) Create(_ context.Context, ch *domain.Chapter) (*domain.Chapter, error) {
	ch.ID = chapter2ID
	r.chapters[ch.ID] = ch
	return ch, nil
}

func (r *fakeChapterRepo) GetByID(_ context.Context, bookID, id string) (*domain.Chapter, error) {
	ch, ok := r.chapters[id]
	if !ok || ch.BookID != bookID {
		return nil, repository.ErrChapterNotFound
	}
	return ch, nil
}

func (r *fakeChapterRepo) ListByBook(_ context.Context, bookID string, publishedOnly bool) ([]*domain.Chapter, error) {
	out := make([]*domain.Chapter, 0)
	for _, ch := range r.chapters {
		if ch.BookID != bookID {
			continue
		}
		if publishedOnly && ch.Status != domain.ChapterStatusPublished {
			continue
		}
		out = append(out, ch)
	}
	return out, nil
}

func (r *fakeChapterRepo) Update(_ context.Context, upd *domain.ChapterUpdate) (*domain.Chapter, error) {
	ch, ok := r.chapters[upd.ID]
	if !ok {
		return nil, repository.ErrChapterNotFound
	}
	if upd.Title != nil {
		ch.Title = *upd.Title
	}
	return ch, nil
}

func (r *fakeChapterRepo) Delete(_ context.Context, _, id string) error {
	if _, ok := r.chapters[id]; !ok {
		return repository.ErrChapterNotFound
	}
	delete(r.chapters, id)
	return nil
}

func (r *fakeChapterRepo) CountByBook(_ context.Context, bookID string) (int, error) {
	n := 0
	for _, ch := range r.chapters {
		if ch.BookID == bookID {
			n++
		}
	}
	return n, nil
}

func (r *fakeChapterRepo) Reorder(_ context.Context, bookID string, ids []string) ([]*domain.Chapter, error) {
	total, _ := r.CountByBook(context.Background(), bookID)
	if total != len(ids) {
		return nil, repository.ErrReorderMismatch
	}
	out := make([]*domain.Chapter, 0, len(ids))
	for i, id := range ids {
		ch, ok := r.chapters[id]
		if !ok || ch.BookID != bookID {
			return nil, repository.ErrReorderMismatch
		}
		ch.Position = int32(i + 1)
		out = append(out, ch)
	}
	return out, nil
}

type fakeSagaRepo struct {
	sagas map[string]*domain.Saga
}

func (r *fakeSagaRepo) Create(_ context.Context, s *domain.Saga) (*domain.Saga, error) {
	s.ID = sagaID
	r.sagas[s.ID] = s
	return s, nil
}

func (r *fakeSagaRepo) GetByID(_ context.Context, id string) (*domain.Saga, error) {
	s, ok := r.sagas[id]
	if !ok {
		return nil, repository.ErrSagaNotFound
	}
	return s, nil
}

func (r *fakeSagaRepo) List(_ context.Context, f domain.SagaFilter) ([]*domain.Saga, int32, error) {
	out := make([]*domain.Saga, 0)
	for _, s := range r.sagas {
		if f.AuthorID != "" && s.AuthorID != f.AuthorID {
			continue
		}
		out = append(out, s)
	}
	return out, int32(len(out)), nil
}

func (r *fakeSagaRepo) Update(_ context.Context, upd *domain.SagaUpdate) (*domain.Saga, error) {
	s, ok := r.sagas[upd.ID]
	if !ok {
		return nil, repository.ErrSagaNotFound
	}
	if upd.Title != nil {
		s.Title = *upd.Title
	}
	return s, nil
}

func (r *fakeSagaRepo) Delete(_ context.Context, id string) error {
	if _, ok := r.sagas[id]; !ok {
		return repository.ErrSagaNotFound
	}
	delete(r.sagas, id)
	return nil
}

func (r *fakeSagaRepo) ListBooks(context.Context, string) ([]*domain.Book, error) {
	return []*domain.Book{}, nil
}
func (r *fakeSagaRepo) AddBook(context.Context, string, string, int32) error { return nil }
func (r *fakeSagaRepo) RemoveBook(context.Context, string, string) error     { return nil }
func (r *fakeSagaRepo) ReorderBooks(context.Context, string, []string) error { return nil }

type fakeSavedRepo struct {
	saved []*domain.SavedBook
}

func (r *fakeSavedRepo) Save(_ context.Context, userID, bookID, kind string) (*domain.SavedBook, error) {
	sb := &domain.SavedBook{ID: "saved-1", UserID: userID, Kind: kind, Book: &domain.Book{ID: bookID}}
	r.saved = append(r.saved, sb)
	return sb, nil
}

func (r *fakeSavedRepo) ListByUser(_ context.Context, userID, kind string) ([]*domain.SavedBook, error) {
	out := make([]*domain.SavedBook, 0)
	for _, sb := range r.saved {
		if sb.UserID != userID {
			continue
		}
		if kind != "" && sb.Kind != kind {
			continue
		}
		out = append(out, sb)
	}
	return out, nil
}

func (r *fakeSavedRepo) Unsave(_ context.Context, userID, bookID, kind string) error {
	return nil
}

// --- Armado ----------------------------------------------------------------

// newTestService arma el servicio con un libro publicado, uno en borrador (los
// dos del mismo autor), dos capitulos y una saga. Es el escenario minimo donde
// se pueden ejercitar todas las reglas de visibilidad.
func newTestService(a Authenticator) (*LibraryService, *noopCache) {
	books := &fakeBookRepo{books: map[string]*domain.Book{
		bookID: {ID: bookID, AuthorID: authorID, Title: "Publicado", Status: domain.BookStatusPublished},
	}}
	chapters := &fakeChapterRepo{chapters: map[string]*domain.Chapter{
		chapterID:  {ID: chapterID, BookID: bookID, Title: "Uno", Position: 1, Status: domain.ChapterStatusPublished},
		chapter2ID: {ID: chapter2ID, BookID: bookID, Title: "Dos", Position: 2, Status: domain.ChapterStatusDraft},
	}}
	sagas := &fakeSagaRepo{sagas: map[string]*domain.Saga{
		sagaID: {ID: sagaID, AuthorID: authorID, Title: "Saga"},
	}}
	c := &noopCache{}
	return NewLibraryService(books, chapters, sagas, &fakeSavedRepo{}, c, a), c
}

// draftBook agrega un libro en borrador del autor al escenario.
func draftBook(s *LibraryService) string {
	const id = "77777777-7777-7777-7777-777777777777"
	s.books.(*fakeBookRepo).books[id] = &domain.Book{
		ID: id, AuthorID: authorID, Title: "Borrador", Status: domain.BookStatusDraft,
	}
	return id
}
