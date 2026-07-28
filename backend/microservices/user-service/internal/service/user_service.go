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
)

// SessionCache y UserCache son lo que el servicio necesita de Valkey. Son
// interfaces y no los tipos concretos para poder probar el cache-aside y la
// invalidacion sin levantar Valkey, y para poder afirmar que una lectura
// cacheada de verdad no toca la BD.
type SessionCache interface {
	Set(ctx context.Context, tokenHash, userID string, ttl time.Duration) error
	Get(ctx context.Context, tokenHash string) (string, error)
	Delete(ctx context.Context, tokenHash string) error
}

type UserCache interface {
	SetUser(ctx context.Context, u *domain.User, ttl time.Duration) error
	GetUser(ctx context.Context, id string) (*domain.User, error)
	SetAllUsers(ctx context.Context, users []*domain.User, ttl time.Duration) error
	GetAllUsers(ctx context.Context) ([]*domain.User, error)
	InvalidateUser(ctx context.Context, id string) error
	InvalidateAllUsers(ctx context.Context) error
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
	_ UserCache    = (*cache.UserCache)(nil)
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
	users        cachedUsers
	content      ContentDeleter
	interactions InteractionDeleter
}

func NewUserService(
	repo repository.UserRepository,
	sessions repository.SessionRepository,
	sessionCache SessionCache,
	userCache UserCache,
	content ContentDeleter,
	interactions InteractionDeleter,
) *UserService {
	return &UserService{
		repo:         repo,
		sessions:     sessions,
		cache:        sessionCache,
		users:        cachedUsers{cache: userCache},
		content:      content,
		interactions: interactions,
	}
}

// userByID resuelve un usuario con cache-aside: primero Valkey, y si hay miss
// va a la BD y repuebla la clave. Lo comparten GetUser y GetCurrentUser, asi
// que ambos aprovechan la misma entrada.
func (s *UserService) userByID(ctx context.Context, id string) (*domain.User, error) {
	if u, hit := s.users.user(ctx, id); hit {
		return u, nil
	}

	u, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err, "failed to get user")
	}

	s.users.storeUser(ctx, u)
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
