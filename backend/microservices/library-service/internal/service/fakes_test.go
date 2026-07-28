package service

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/cache"
	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/domain"
	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/repository"
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
	// El id que le toca a un libro recien creado, distinto de bookID para que
	// CreateBook no pise el libro del escenario.
	newBookID = "88888888-8888-8888-8888-888888888888"
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
	// chapters lo enlaza newTestService para poder calcular ChapterCount igual
	// que lo hace la subconsulta real.
	chapters *fakeChapterRepo
	// getByIDCalls cuenta los viajes a la "BD". Es lo unico con lo que se puede
	// afirmar que una lectura servida desde el cache de verdad no consulto.
	getByIDCalls int
}

// countChapters replica chapterCountExpr: solo los publicados, para todos por
// igual.
func (r *fakeBookRepo) countChapters(bookID string) int32 {
	if r.chapters == nil {
		return 0
	}

	n := int32(0)
	for _, ch := range r.chapters.chapters {
		if ch.BookID == bookID && ch.Status == domain.ChapterStatusPublished {
			n++
		}
	}
	return n
}

// withCount devuelve una copia con el conteo ya resuelto: el repositorio real
// tampoco guarda ese numero en la fila, lo calcula en cada consulta.
func (r *fakeBookRepo) withCount(b *domain.Book) *domain.Book {
	out := *b
	out.ChapterCount = r.countChapters(b.ID)
	return &out
}

// Create resuelve los generos contra el catalogo del doble, igual que el
// repositorio real contra la FK: un slug que no esta hace fallar el alta entera.
func (r *fakeBookRepo) Create(_ context.Context, b *domain.Book, genres []string) (*domain.Book, error) {
	resolved, err := resolveGenres(genres)
	if err != nil {
		return nil, err
	}

	b.ID = newBookID
	b.CreatedAt = time.Now()
	b.UpdatedAt = b.CreatedAt
	b.Genres = resolved
	r.books[b.ID] = b
	return b, nil
}

func (r *fakeBookRepo) GetByID(_ context.Context, id string) (*domain.Book, error) {
	r.getByIDCalls++
	b, ok := r.books[id]
	if !ok {
		return nil, repository.ErrBookNotFound
	}
	return r.withCount(b), nil
}

// List modela tambien el recorte por pagina, porque de eso depende la
// diferencia entre los dos listados: el publico pagina y el de lo propio no.
// total es siempre cuantos cumplen el filtro, no cuantos trae la pagina.
func (r *fakeBookRepo) List(_ context.Context, f domain.BookFilter) ([]*domain.Book, int32, error) {
	out := make([]*domain.Book, 0)
	for _, b := range r.books {
		if f.AuthorID != "" && b.AuthorID != f.AuthorID {
			continue
		}
		if f.Status != "" && b.Status != f.Status {
			continue
		}
		out = append(out, r.withCount(b))
	}
	// El mapa no tiene orden; el repositorio real ordena por created_at. Aqui
	// alcanza con que el recorte sea reproducible.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	total := int32(len(out))
	// PageSize en 0 es "todo, sin LIMIT", igual que en la consulta real.
	if f.PageSize > 0 {
		desde := (f.Page - 1) * f.PageSize
		if desde > total {
			desde = total
		}
		hasta := desde + f.PageSize
		if hasta > total {
			hasta = total
		}
		out = out[desde:hasta]
	}
	return out, total, nil
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
		// Igual que en los capitulos: replica el COALESCE del repositorio.
		if *upd.Status == domain.BookStatusPublished && b.PublishedAt == nil {
			ahora := time.Now()
			b.PublishedAt = &ahora
		}
	}
	return r.withCount(b), nil
}

func (r *fakeBookRepo) Delete(_ context.Context, id string) error {
	if _, ok := r.books[id]; !ok {
		return repository.ErrBookNotFound
	}
	delete(r.books, id)
	return nil
}

func (r *fakeBookRepo) IDsByAuthor(_ context.Context, authorID string) ([]string, error) {
	ids := make([]string, 0)
	for id, b := range r.books {
		if b.AuthorID == authorID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// DeleteByAuthor imita el borrado en cascada de la baja de cuenta. El conteo de
// sagas no lo modela: lo que importa aqui es que los libros del autor
// desaparezcan y los ajenos no.
func (r *fakeBookRepo) DeleteByAuthor(_ context.Context, authorID string) (int32, int32, error) {
	borrados := int32(0)
	for id, b := range r.books {
		if b.AuthorID == authorID {
			delete(r.books, id)
			borrados++
		}
	}
	return borrados, 0, nil
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
	if upd.Content != nil {
		ch.Content = upd.Content
	}
	// El status se aplica de verdad: de el depende la regla del ultimo
	// capitulo publicado.
	if upd.Status != nil {
		ch.Status = *upd.Status
		// Replica el COALESCE del repositorio: la fecha se escribe la primera
		// vez y no se pisa despues.
		if *upd.Status == domain.ChapterStatusPublished && ch.PublishedAt == nil {
			ahora := time.Now()
			ch.PublishedAt = &ahora
		}
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

// catalogo es el equivalente en memoria de content.genres: la lista cerrada
// contra la que se resuelven los slugs. No hace falta que sea la real, solo que
// tenga mas de cuatro entradas para poder pasarse del tope.
var catalogo = []*domain.Genre{
	{Slug: "fantasy", Name: "Fantasía"},
	{Slug: "horror", Name: "Terror"},
	{Slug: "romance", Name: "Romance"},
	{Slug: "drama", Name: "Drama"},
	{Slug: "comedy", Name: "Comedia"},
}

// resolveGenres hace lo que la FK contra content.genres: un slug fuera del
// catalogo no se escribe, devuelve ErrGenreNotFound.
func resolveGenres(slugs []string) ([]*domain.Genre, error) {
	out := make([]*domain.Genre, 0, len(slugs))
	for _, slug := range slugs {
		encontrado := false
		for _, g := range catalogo {
			if g.Slug == slug {
				out = append(out, g)
				encontrado = true
				break
			}
		}
		if !encontrado {
			return nil, repository.ErrGenreNotFound
		}
	}
	// El trigger de la BD es la ultima palabra sobre el tope, aunque al doble
	// solo se llegue con listas que el servicio ya valido.
	if len(out) > domain.GenreMaxPerBook {
		return nil, repository.ErrTooManyGenres
	}
	return out, nil
}

type fakeGenreRepo struct {
	// books lo enlaza newTestService: escribir los generos tiene que verse en la
	// siguiente lectura del libro, que es de donde SetBookGenres saca lo que
	// devuelve.
	books *fakeBookRepo
}

func (r *fakeGenreRepo) List(context.Context) ([]*domain.Genre, error) {
	return catalogo, nil
}

func (r *fakeGenreRepo) ReplaceForBook(_ context.Context, bookID string, slugs []string) error {
	resolved, err := resolveGenres(slugs)
	if err != nil {
		return err
	}
	b, ok := r.books.books[bookID]
	if !ok {
		return repository.ErrBookNotFound
	}
	b.Genres = resolved
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
	// El conteo de capitulos lo resuelve la consulta de libros, asi que el
	// doble necesita ver los capitulos para calcularlo.
	books.chapters = chapters
	sagas := &fakeSagaRepo{sagas: map[string]*domain.Saga{
		sagaID: {ID: sagaID, AuthorID: authorID, Title: "Saga"},
	}}
	c := &noopCache{}
	svc := NewLibraryService(books, chapters, sagas, &fakeGenreRepo{books: books}, c, a, &fakeInteractions{})
	return svc, c
}

// fakeInteractions registra a que libros se les pidio limpiar las
// interacciones, para poder afirmar que borrar un libro se las lleva.
//
// Lleva mutex porque la baja de cuenta pide las limpiezas en paralelo: el doble
// tiene que aguantar lo mismo que aguanta el cliente gRPC real. Por eso mismo el
// orden de `cleaned` no significa nada y las pruebas no deben afirmarlo.
type fakeInteractions struct {
	mu      sync.Mutex
	cleaned []string
	err     error
}

func (f *fakeInteractions) DeleteBookInteractions(_ context.Context, bookID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.err != nil {
		return f.err
	}
	f.cleaned = append(f.cleaned, bookID)
	return nil
}

// draftBook agrega un libro en borrador del autor al escenario.
func draftBook(s *LibraryService) string {
	return bookConEstado(s, domain.BookStatusDraft)
}

// bookConEstado agrega al escenario un libro del autor en el estado que se pida.
// Es lo que necesitan las pruebas de la regla de publicacion de capitulos, que
// tienen que probar los tres estados del libro.
func bookConEstado(s *LibraryService, estado string) string {
	const id = "77777777-7777-7777-7777-777777777777"
	s.books.(*fakeBookRepo).books[id] = &domain.Book{
		ID: id, AuthorID: authorID, Title: "Otro libro", Status: estado,
	}
	return id
}

// capituloDe cuelga un capitulo del libro que se le indique y devuelve su id.
func capituloDe(s *LibraryService, bookID, estado string) string {
	const id = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	s.chapters.(*fakeChapterRepo).chapters[id] = &domain.Chapter{
		ID: id, BookID: bookID, Title: "Capitulo", Position: 1, Status: estado,
	}
	return id
}
