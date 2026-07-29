package service

import (
	"context"

	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/auth"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/content"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/events"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/repository"
	interactionv1 "github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/proto/interaction/v1"
	"github.com/EstebanTech/lectonautas/backend/shared/cache"
)

// Authenticator resuelve la identidad del llamante. Es una interfaz y no el
// tipo concreto para poder probar las reglas de propiedad —quien puede borrar
// que— sin levantar user-service.
type Authenticator interface {
	// Require exige un token valido y devuelve el user_id.
	Require(ctx context.Context) (string, error)
	// Optional devuelve cadena vacia si no vino token, y error si vino uno
	// invalido.
	Optional(ctx context.Context) (string, error)
}

// Books es lo que el servicio necesita saber de library-service: si el libro
// existe, esta publicado y de quien es.
type Books interface {
	PublishedBook(ctx context.Context, bookID string) (*content.Book, error)
}

// Cache es lo que el servicio necesita de Valkey. El servicio no lo usa
// directo: lo envuelve cache.Aside, que le pone la politica de fallos.
type Cache interface {
	Key(ctx context.Context, parts ...string) (string, error)
	Get(ctx context.Context, key string, dest any) error
	Set(ctx context.Context, key string, value any) error
	Bump(ctx context.Context) error
}

// Las implementaciones reales tienen que seguir cumpliendo el contrato.
var (
	_ Authenticator = (*auth.Authenticator)(nil)
	_ Books         = (*content.Client)(nil)
	_ Cache         = (*cache.Versioned)(nil)
)

// InteractionService implementa la API del servicio. Ademas de responder, cada
// escritura publica un evento en el bus: eso es lo que hace que el WebSocket
// tenga algo que contar.
//
// Los handlers estan repartidos por entidad (like.go, comment.go, rating.go,
// summary.go, cleanup.go); aqui queda el armado. Lo que comparten esta en sus
// propios colaboradores: cache.Aside para el cache, validation.go para los
// campos y errors.go para la traduccion de errores.
type InteractionService struct {
	interactionv1.UnimplementedInteractionServiceServer
	likes    repository.LikeRepository
	comments repository.CommentRepository
	ratings  repository.RatingRepository
	cleanup  repository.CleanupRepository
	books    Books
	cache    cache.Aside
	auth     Authenticator
	bus      events.Publisher
}

func NewInteractionService(
	likes repository.LikeRepository,
	comments repository.CommentRepository,
	ratings repository.RatingRepository,
	cleanup repository.CleanupRepository,
	books Books,
	interactionCache Cache,
	authenticator Authenticator,
	bus events.Publisher,
) *InteractionService {
	return &InteractionService{
		likes:    likes,
		comments: comments,
		ratings:  ratings,
		cleanup:  cleanup,
		books:    books,
		cache:    cache.NewAside(interactionCache),
		auth:     authenticator,
		bus:      bus,
	}
}

// writable resuelve de una vez las dos condiciones de toda escritura: que el
// llamante tenga sesion y que el libro admita interacciones. Devuelve el
// user_id y el libro.
//
// El orden importa: primero el token. Si se comprobara antes el libro, este
// endpoint le diria a cualquiera sin credenciales que un id de libro existe o
// no, que es informacion que no le toca dar a quien no se identifico.
func (s *InteractionService) writable(ctx context.Context, bookID string) (string, *content.Book, error) {
	userID, err := s.auth.Require(ctx)
	if err != nil {
		return "", nil, err
	}

	book, err := s.books.PublishedBook(ctx, bookID)
	if err != nil {
		return "", nil, mapBookErr(ctx, err)
	}
	return userID, book, nil
}
