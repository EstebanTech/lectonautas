package service

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/cache"
	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/domain"
	libraryv1 "github.com/EstebanTech/lectonautas/backend/microservices/library-service/proto/library/v1"
)

// Estas pruebas son las unicas que corren con un cache que de verdad guarda. El
// resto usa noopCache a proposito, para que las reglas de negocio se afirmen
// contra el repositorio; aqui lo que se afirma es justo lo otro: que un acierto
// de cache sirve la version que le toca a quien pregunta, y solo esa.
//
// Importa porque GetBook, GetChapter y GetSaga resuelven la visibilidad desde la
// propia entrada cacheada cuando quien pregunta no es el autor, y se saltan la
// consulta. Ese atajo es lo que hace que la lectura publica no toque Postgres, y
// tambien lo que podria filtrar un borrador si estuviera mal.

// memCache imita a LibraryCache: claves versionadas por un contador que Bump
// incrementa, y valores serializados como JSON. Sin la version no se podria
// probar la invalidacion, que es de lo que depende que el atajo sea correcto.
type memCache struct {
	version int
	entries map[string][]byte
}

func newMemCache() *memCache {
	return &memCache{entries: map[string][]byte{}}
}

func (c *memCache) Key(_ context.Context, parts ...string) (string, error) {
	return "v" + strconv.Itoa(c.version) + ":" + strings.Join(parts, ":"), nil
}

func (c *memCache) Get(_ context.Context, key string, dest any) error {
	payload, ok := c.entries[key]
	if !ok {
		return cache.ErrMiss
	}
	return json.Unmarshal(payload, dest)
}

func (c *memCache) Set(_ context.Context, key string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.entries[key] = payload
	return nil
}

func (c *memCache) Bump(context.Context) error {
	c.version++
	return nil
}

// cachedHarness es newTestService pero con el cache real y acceso a los dobles,
// para poder contar consultas y cambiar el estado del libro a media prueba.
type cachedHarness struct {
	svc      *LibraryService
	books    *fakeBookRepo
	chapters *fakeChapterRepo
	cache    *memCache
}

func newCachedService(a Authenticator) *cachedHarness {
	books := &fakeBookRepo{books: map[string]*domain.Book{
		bookID: {ID: bookID, AuthorID: authorID, Title: "Publicado", Status: domain.BookStatusPublished},
	}}
	chapters := &fakeChapterRepo{chapters: map[string]*domain.Chapter{
		chapterID:  {ID: chapterID, BookID: bookID, Title: "Uno", Position: 1, Status: domain.ChapterStatusPublished},
		chapter2ID: {ID: chapter2ID, BookID: bookID, Title: "Dos", Position: 2, Status: domain.ChapterStatusDraft},
	}}
	books.chapters = chapters
	sagas := &fakeSagaRepo{sagas: map[string]*domain.Saga{
		sagaID: {ID: sagaID, AuthorID: authorID, Title: "Saga"},
	}}
	c := newMemCache()

	return &cachedHarness{
		svc:      NewLibraryService(books, chapters, sagas, &fakeGenreRepo{books: books}, c, a, &fakeInteractions{}),
		books:    books,
		chapters: chapters,
		cache:    c,
	}
}

// --- El atajo rinde -----------------------------------------------------------

func TestGetBook_LectorAjenoCacheadoNoTocaLaBD(t *testing.T) {
	h := newCachedService(anonymous())
	ctx := context.Background()

	// Primera lectura: llena el cache y si consulta.
	if _, err := h.svc.GetBook(ctx, &libraryv1.GetBookRequest{Id: bookID}); err != nil {
		t.Fatalf("primera lectura: %v", err)
	}
	if h.books.getByIDCalls == 0 {
		t.Fatal("la primera lectura tenia que consultar el libro")
	}

	h.books.getByIDCalls = 0

	// Segunda lectura: tiene que salir entera del cache.
	resp, err := h.svc.GetBook(ctx, &libraryv1.GetBookRequest{Id: bookID})
	if err != nil {
		t.Fatalf("segunda lectura: %v", err)
	}
	if h.books.getByIDCalls != 0 {
		t.Errorf("la lectura cacheada consulto el libro %d veces; tenia que ser 0",
			h.books.getByIDCalls)
	}
	if len(resp.GetChapters()) != 1 {
		t.Errorf("el lector ajeno vio %d capitulos; solo tenia que ver el publicado",
			len(resp.GetChapters()))
	}
}

func TestGetChapter_LectorAjenoCacheadoNoTocaLaBD(t *testing.T) {
	h := newCachedService(anonymous())
	ctx := context.Background()
	req := &libraryv1.GetChapterRequest{BookId: bookID, Id: chapterID}

	if _, err := h.svc.GetChapter(ctx, req); err != nil {
		t.Fatalf("primera lectura: %v", err)
	}
	h.books.getByIDCalls = 0

	if _, err := h.svc.GetChapter(ctx, req); err != nil {
		t.Fatalf("segunda lectura: %v", err)
	}
	if h.books.getByIDCalls != 0 {
		t.Errorf("la lectura cacheada consulto el libro %d veces; tenia que ser 0",
			h.books.getByIDCalls)
	}
}

func TestGetSaga_LectorAjenoCacheadoNoTocaLaBD(t *testing.T) {
	h := newCachedService(anonymous())
	ctx := context.Background()
	req := &libraryv1.GetSagaRequest{Id: sagaID}

	if _, err := h.svc.GetSaga(ctx, req); err != nil {
		t.Fatalf("primera lectura: %v", err)
	}
	before := h.books.getByIDCalls

	if _, err := h.svc.GetSaga(ctx, req); err != nil {
		t.Fatalf("segunda lectura: %v", err)
	}
	if h.books.getByIDCalls != before {
		t.Errorf("la lectura cacheada volvio a consultar; llamadas %d -> %d",
			before, h.books.getByIDCalls)
	}
}

// --- El atajo no filtra -------------------------------------------------------

// El autor no puede quedarse con la version publica que dejo cacheada un lector:
// es la que oculta sus borradores.
func TestGetBook_ElAutorNoSeSirveDeLaEntradaPublica(t *testing.T) {
	lector := newCachedService(anonymous())
	ctx := context.Background()

	// Un lector ajeno calienta la entrada publica.
	if _, err := lector.svc.GetBook(ctx, &libraryv1.GetBookRequest{Id: bookID}); err != nil {
		t.Fatalf("lectura del lector: %v", err)
	}

	// El autor pregunta por el mismo libro, contra el mismo cache.
	autor := NewLibraryService(lector.books, lector.chapters,
		&fakeSagaRepo{sagas: map[string]*domain.Saga{}}, &fakeGenreRepo{books: lector.books},
		lector.cache, asAuthor(), &fakeInteractions{})

	resp, err := autor.GetBook(ctx, &libraryv1.GetBookRequest{Id: bookID})
	if err != nil {
		t.Fatalf("lectura del autor: %v", err)
	}
	if len(resp.GetChapters()) != 2 {
		t.Errorf("el autor vio %d capitulos; tenia que ver los 2, incluido el borrador",
			len(resp.GetChapters()))
	}
}

// Despublicar tiene que dejar inalcanzable la entrada publica. Si el atajo se
// sirviera de una clave que sobrevive a la escritura, un libro retirado seguiria
// abriendose para cualquiera.
func TestGetBook_DespublicarInvalidaLaEntradaPublica(t *testing.T) {
	h := newCachedService(anonymous())
	ctx := context.Background()

	if _, err := h.svc.GetBook(ctx, &libraryv1.GetBookRequest{Id: bookID}); err != nil {
		t.Fatalf("primera lectura: %v", err)
	}

	// El autor lo pasa a borrador por la via normal, que invalida el cache.
	autor := NewLibraryService(h.books, h.chapters,
		&fakeSagaRepo{sagas: map[string]*domain.Saga{}}, &fakeGenreRepo{books: h.books},
		h.cache, asAuthor(), &fakeInteractions{})
	draft := domain.BookStatusDraft
	if _, err := autor.UpdateBook(ctx, &libraryv1.UpdateBookRequest{Id: bookID, Status: &draft}); err != nil {
		t.Fatalf("despublicar: %v", err)
	}

	// El lector ajeno ya no puede verlo, ni desde el cache.
	if _, err := h.svc.GetBook(ctx, &libraryv1.GetBookRequest{Id: bookID}); err == nil {
		t.Fatal("el lector ajeno pudo abrir un libro despublicado desde el cache")
	}
}

// Lo mismo para el capitulo: es el que mas se lee y el que lleva el texto.
func TestGetChapter_DespublicarElLibroInvalidaLaEntradaPublica(t *testing.T) {
	h := newCachedService(anonymous())
	ctx := context.Background()
	req := &libraryv1.GetChapterRequest{BookId: bookID, Id: chapterID}

	if _, err := h.svc.GetChapter(ctx, req); err != nil {
		t.Fatalf("primera lectura: %v", err)
	}

	autor := NewLibraryService(h.books, h.chapters,
		&fakeSagaRepo{sagas: map[string]*domain.Saga{}}, &fakeGenreRepo{books: h.books},
		h.cache, asAuthor(), &fakeInteractions{})
	draft := domain.BookStatusDraft
	if _, err := autor.UpdateBook(ctx, &libraryv1.UpdateBookRequest{Id: bookID, Status: &draft}); err != nil {
		t.Fatalf("despublicar: %v", err)
	}

	if _, err := h.svc.GetChapter(ctx, req); err == nil {
		t.Fatal("el lector ajeno pudo abrir un capitulo de un libro despublicado desde el cache")
	}
}

// El autor tampoco puede quedarse con la version publica de un capitulo: para el
// no hay diferencia de contenido, pero si la hay de alcance, y la clave que se
// escriba tiene que ser la suya.
func TestGetChapter_ElAutorVeSuBorradorConLaEntradaPublicaCaliente(t *testing.T) {
	h := newCachedService(anonymous())
	ctx := context.Background()

	// El lector calienta la entrada publica del capitulo publicado.
	if _, err := h.svc.GetChapter(ctx, &libraryv1.GetChapterRequest{BookId: bookID, Id: chapterID}); err != nil {
		t.Fatalf("lectura del lector: %v", err)
	}

	autor := NewLibraryService(h.books, h.chapters,
		&fakeSagaRepo{sagas: map[string]*domain.Saga{}}, &fakeGenreRepo{books: h.books},
		h.cache, asAuthor(), &fakeInteractions{})

	// El autor pide el capitulo en borrador, que el lector no puede ni ver.
	resp, err := autor.GetChapter(ctx, &libraryv1.GetChapterRequest{BookId: bookID, Id: chapter2ID})
	if err != nil {
		t.Fatalf("el autor no pudo abrir su borrador: %v", err)
	}
	if resp.GetChapter().GetId() != chapter2ID {
		t.Errorf("el autor recibio el capitulo %q; esperaba su borrador %q",
			resp.GetChapter().GetId(), chapter2ID)
	}
}
