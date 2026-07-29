package service

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/cache"
	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/domain"
	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/repository"
	userv1 "github.com/EstebanTech/lectonautas/backend/microservices/user-service/proto/user/v1"
	sharedcache "github.com/EstebanTech/lectonautas/backend/shared/cache"
)

// SessionCache y Cache son lo que el servicio necesita de Valkey. Son
// interfaces y no los tipos concretos para poder probar el cache-aside y la
// invalidacion sin levantar Valkey, y para poder afirmar que una lectura
// cacheada de verdad no toca la BD.
//
// Son dos y no una porque cachean cosas distintas con reglas distintas: los
// datos de usuario caducan (frescura) y las sesiones se revocan (una entrada
// concreta, al instante). Ver el comentario del paquete internal/cache.
type SessionCache interface {
	Set(ctx context.Context, tokenHash, userID string, ttl time.Duration) error
	Get(ctx context.Context, tokenHash string) (string, error)
	Delete(ctx context.Context, tokenHash string) error
}

// Cache es el cache versionado compartido, el mismo que usan library-service e
// interaction-service: las claves llevan dentro un contador y cualquier
// escritura lo incrementa, dejando lo viejo inalcanzable de golpe.
type Cache interface {
	Key(ctx context.Context, parts ...string) (string, error)
	Get(ctx context.Context, key string, dest any) error
	Set(ctx context.Context, key string, value any) error
	Bump(ctx context.Context) error
}

// ContentDeleter borra en library-service todo lo que cuelga de un usuario. Es
// el otro lado de la baja de cuenta: los libros, sagas y guardados viven en otra
// base de datos, asi que no hay un CASCADE de Postgres que los alcance y hay que
// pedirselo al servicio dueno.
//
// Es una interfaz para poder probar la baja sin levantar library-service.
type ContentDeleter interface {
	// DeleteAuthorContent tiene que ser idempotente: si el borrado del usuario
	// falla despues, el cliente reintenta y esto se vuelve a llamar.
	DeleteAuthorContent(ctx context.Context, userID string) error
}

// InteractionDeleter borra en interaction-service lo que el usuario dejo como
// lector: sus me gusta, sus comentarios y sus calificaciones. Es el mismo caso
// que ContentDeleter — otra base de datos, ningun CASCADE que la alcance— pero
// para el otro lado de la cuenta: no lo que escribio, sino lo que dijo sobre lo
// que escribieron otros.
type InteractionDeleter interface {
	// Como DeleteAuthorContent, tiene que ser idempotente.
	DeleteUserInteractions(ctx context.Context, userID string) error
}

// Las implementaciones reales tienen que seguir cumpliendo el contrato.
var (
	_ SessionCache = (*cache.SessionCache)(nil)
	_ Cache        = (*sharedcache.Versioned)(nil)
)

// UserService implementa la API del servicio. Los handlers no viven todos aqui:
// este archivo tiene el armado y lo que comparten, las altas y bajas de cuenta
// estan en user.go y todo lo que es sesion (login, logout, validacion) en
// auth.go.
type UserService struct {
	userv1.UnimplementedUserServiceServer
	repo         repository.UserRepository
	sessions     repository.SessionRepository
	cache        SessionCache
	users        sharedcache.Aside
	content      ContentDeleter
	interactions InteractionDeleter
}

func NewUserService(
	repo repository.UserRepository,
	sessions repository.SessionRepository,
	sessionCache SessionCache,
	userCache Cache,
	content ContentDeleter,
	interactions InteractionDeleter,
) *UserService {
	return &UserService{
		repo:         repo,
		sessions:     sessions,
		cache:        sessionCache,
		users:        sharedcache.NewAside(userCache),
		content:      content,
		interactions: interactions,
	}
}

// userByID resuelve un usuario con cache-aside: primero Valkey, y si hay miss
// va a la BD y repuebla la clave. Lo comparten GetUser y GetCurrentUser, asi
// que ambos aprovechan la misma entrada.
func (s *UserService) userByID(ctx context.Context, id string) (*domain.User, error) {
	var cached cachedUser
	key, hit := s.users.Get(ctx, &cached, userKeyPart, id)
	if hit {
		return cached.toDomain(), nil
	}

	u, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, mapRepoErr(ctx, err, "failed to get user")
	}

	s.users.Set(ctx, key, toCached(u))
	return u, nil
}

// requireSelf exige un token valido y que sea el del propio usuario que se va a
// tocar. Responde PermissionDenied (y no NotFound) porque la existencia de la
// cuenta ya es publica: GetUser la devuelve sin token.
func (s *UserService) requireSelf(ctx context.Context, id string) error {
	callerID, err := s.authenticate(ctx)
	if err != nil {
		return err
	}
	if callerID != id {
		return status.Error(codes.PermissionDenied, "you can only modify your own account")
	}
	return nil
}
