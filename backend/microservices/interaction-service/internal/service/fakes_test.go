package service

import (
	"context"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/content"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/domain"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/events"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/repository"
	"github.com/EstebanTech/lectonautas/backend/shared/cache"
)

// Dobles en memoria de las dependencias del servicio. Solo implementan lo que
// las pruebas ejercitan; lo que no, falla explicitamente para que una prueba
// que dependa de algo no modelado no pase por accidente.

// UUIDs fijos para que las pruebas se lean sin ruido.
const (
	authorID   = "11111111-1111-1111-1111-111111111111"
	readerID   = "22222222-2222-2222-2222-222222222222"
	intruderID = "33333333-3333-3333-3333-333333333333"
	bookID     = "44444444-4444-4444-4444-444444444444"
	draftID    = "55555555-5555-5555-5555-555555555555"
	missingID  = "66666666-6666-6666-6666-666666666666"
	commentID  = "77777777-7777-7777-7777-777777777777"
)

// --- Authenticator ------------------------------------------------------------

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

func anonymous() fakeAuth  { return fakeAuth{} }
func asAuthor() fakeAuth   { return fakeAuth{userID: authorID} }
func asReader() fakeAuth   { return fakeAuth{userID: readerID} }
func asIntruder() fakeAuth { return fakeAuth{userID: intruderID} }

// --- library-service ----------------------------------------------------------

// fakeBooks modela lo unico que importa del vecino: que un libro publicado se
// resuelve y cualquier otra cosa —borrador o inexistente— responde NotFound,
// que es lo que hace library-service cuando se le pregunta sin token.
type fakeBooks struct {
	// down simula al vecino caido, que no es lo mismo que "el libro no existe".
	down bool
	// calls cuenta las consultas, para poder afirmar que las lecturas publicas
	// no molestan a library-service en el camino caliente.
	calls int
}

func (f *fakeBooks) PublishedBook(_ context.Context, id string) (*content.Book, error) {
	f.calls++
	if f.down {
		return nil, status.Error(codes.Unavailable, "library-service is down")
	}
	if id != bookID {
		return nil, status.Error(codes.NotFound, "book not found")
	}
	return &content.Book{ID: bookID, AuthorID: authorID, Status: "published"}, nil
}

// --- Cache ----------------------------------------------------------------------

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

// --- Bus ------------------------------------------------------------------------

// fakeBus se queda con lo publicado para poder afirmar que cada escritura avisa
// por el WebSocket, que es la mitad del sentido de este servicio.
type fakeBus struct{ published []events.Event }

func (b *fakeBus) Publish(_ context.Context, evt events.Event) {
	b.published = append(b.published, evt)
}

// last devuelve el ultimo evento, o el cero si no hubo ninguno.
func (b *fakeBus) last() events.Event {
	if len(b.published) == 0 {
		return events.Event{}
	}
	return b.published[len(b.published)-1]
}

// --- Repositorios -----------------------------------------------------------------

// likeKey es la PK compuesta de la tabla, replicada en memoria.
type likeKey struct{ book, user string }

type fakeLikeRepo struct {
	likes map[likeKey]bool
}

func (r *fakeLikeRepo) Like(ctx context.Context, book, user string) (*domain.LikeSummary, error) {
	// Sin comprobar si ya estaba: eso es lo que hace el ON CONFLICT DO NOTHING
	// del repositorio real, y es lo que sostiene la idempotencia.
	r.likes[likeKey{book, user}] = true
	return r.Summary(ctx, book, user)
}

func (r *fakeLikeRepo) Unlike(ctx context.Context, book, user string) (*domain.LikeSummary, error) {
	delete(r.likes, likeKey{book, user})
	return r.Summary(ctx, book, user)
}

func (r *fakeLikeRepo) Summary(_ context.Context, book, viewer string) (*domain.LikeSummary, error) {
	s := &domain.LikeSummary{BookID: book}
	for k := range r.likes {
		if k.book != book {
			continue
		}
		s.Count++
		if viewer != "" && k.user == viewer {
			s.LikedByMe = true
		}
	}
	return s, nil
}

type fakeCommentRepo struct {
	comments map[string]*domain.Comment
	// nextID hace que dos comentarios seguidos no compartan id.
	nextID int
}

func (r *fakeCommentRepo) Create(_ context.Context, c *domain.Comment) (*domain.Comment, error) {
	r.nextID++
	c.ID = commentID
	if r.nextID > 1 {
		c.ID = strings.Repeat("8", 8) + "-8888-8888-8888-" + strings.Repeat("8", 11) + string(rune('0'+r.nextID))
	}
	c.CreatedAt = time.Now()
	c.UpdatedAt = c.CreatedAt
	r.comments[c.ID] = c
	return c, nil
}

func (r *fakeCommentRepo) GetByID(_ context.Context, book, id string) (*domain.Comment, error) {
	c, ok := r.comments[id]
	if !ok || c.BookID != book {
		return nil, repository.ErrCommentNotFound
	}
	return c, nil
}

func (r *fakeCommentRepo) ListByBook(_ context.Context, f domain.CommentFilter) ([]*domain.Comment, int32, error) {
	out := make([]*domain.Comment, 0)
	for _, c := range r.comments {
		if c.BookID == f.BookID {
			out = append(out, c)
		}
	}
	// El repositorio real ordena por created_at DESC; el mapa no tiene orden, y
	// sin esto el recorte por pagina no seria reproducible.
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })

	total := int32(len(out))
	desde := (f.Page - 1) * f.PageSize
	if desde > total {
		desde = total
	}
	hasta := desde + f.PageSize
	if hasta > total {
		hasta = total
	}
	return out[desde:hasta], total, nil
}

func (r *fakeCommentRepo) Update(_ context.Context, book, id, body string) (*domain.Comment, error) {
	c, ok := r.comments[id]
	if !ok || c.BookID != book {
		return nil, repository.ErrCommentNotFound
	}
	c.Body = body
	// Se adelanta el reloj para que Edited() sea true: en el repositorio real lo
	// hace el now() del UPDATE, que siempre cae despues del INSERT.
	c.UpdatedAt = c.CreatedAt.Add(time.Minute)
	return c, nil
}

func (r *fakeCommentRepo) Delete(_ context.Context, book, id string) error {
	c, ok := r.comments[id]
	if !ok || c.BookID != book {
		return repository.ErrCommentNotFound
	}
	delete(r.comments, id)
	return nil
}

func (r *fakeCommentRepo) CountByBook(_ context.Context, book string) (int32, error) {
	n := int32(0)
	for _, c := range r.comments {
		if c.BookID == book {
			n++
		}
	}
	return n, nil
}

type ratingKey struct{ book, user string }

type fakeRatingRepo struct {
	scores map[ratingKey]int32
}

func (r *fakeRatingRepo) Rate(ctx context.Context, book, user string, score int32) (*domain.RatingSummary, error) {
	// Asignar sobre la misma clave es el upsert: volver a votar reemplaza.
	r.scores[ratingKey{book, user}] = score
	return r.Summary(ctx, book, user)
}

func (r *fakeRatingRepo) Delete(_ context.Context, book, user string) error {
	k := ratingKey{book, user}
	if _, ok := r.scores[k]; !ok {
		return repository.ErrRatingNotFound
	}
	delete(r.scores, k)
	return nil
}

func (r *fakeRatingRepo) Summary(_ context.Context, book, viewer string) (*domain.RatingSummary, error) {
	s := &domain.RatingSummary{BookID: book}
	var suma int32
	for k, score := range r.scores {
		if k.book != book {
			continue
		}
		s.Count++
		suma += score
		if viewer != "" && k.user == viewer {
			s.MyScore = score
		}
	}
	if s.Count > 0 {
		s.Average = float64(suma) / float64(s.Count)
	}
	return s, nil
}

type fakeCleanupRepo struct {
	likes    *fakeLikeRepo
	comments *fakeCommentRepo
	ratings  *fakeRatingRepo
}

func (r *fakeCleanupRepo) DeleteByUser(_ context.Context, userID string) (repository.Counts, error) {
	return r.deleteMatching(func(book, user string) bool { return user == userID })
}

func (r *fakeCleanupRepo) DeleteByBook(_ context.Context, id string) (repository.Counts, error) {
	return r.deleteMatching(func(book, user string) bool { return book == id })
}

func (r *fakeCleanupRepo) deleteMatching(match func(book, user string) bool) (repository.Counts, error) {
	var out repository.Counts

	for k := range r.likes.likes {
		if match(k.book, k.user) {
			delete(r.likes.likes, k)
			out.Likes++
		}
	}
	for id, c := range r.comments.comments {
		if match(c.BookID, c.UserID) {
			delete(r.comments.comments, id)
			out.Comments++
		}
	}
	for k := range r.ratings.scores {
		if match(k.book, k.user) {
			delete(r.ratings.scores, k)
			out.Ratings++
		}
	}
	return out, nil
}

// --- Armado ---------------------------------------------------------------------

// testService arma el servicio con los dobles y devuelve las piezas sobre las
// que las pruebas afirman.
type testService struct {
	svc      *InteractionService
	cache    *noopCache
	bus      *fakeBus
	books    *fakeBooks
	likes    *fakeLikeRepo
	comments *fakeCommentRepo
	ratings  *fakeRatingRepo
}

// newTestService parte de un libro publicado sin ninguna interaccion todavia.
func newTestService(a Authenticator) *testService {
	likes := &fakeLikeRepo{likes: map[likeKey]bool{}}
	comments := &fakeCommentRepo{comments: map[string]*domain.Comment{}}
	ratings := &fakeRatingRepo{scores: map[ratingKey]int32{}}
	cleanup := &fakeCleanupRepo{likes: likes, comments: comments, ratings: ratings}
	books := &fakeBooks{}
	c := &noopCache{}
	bus := &fakeBus{}

	return &testService{
		svc:      NewInteractionService(likes, comments, ratings, cleanup, books, c, a, bus),
		cache:    c,
		bus:      bus,
		books:    books,
		likes:    likes,
		comments: comments,
		ratings:  ratings,
	}
}

// comentarioDe deja un comentario ya escrito por userID en el escenario.
func (t *testService) comentarioDe(userID, body string) *domain.Comment {
	c, _ := t.comments.Create(context.Background(), &domain.Comment{
		BookID: bookID, UserID: userID, Body: body,
	})
	return c
}
