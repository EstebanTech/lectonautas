package service

import (
	"context"
	"time"

	"github.com/estebandeveloper20/lectonautas/backend/microservices/user-service/internal/cache"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/user-service/internal/domain"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/user-service/internal/repository"
)

// Dobles en memoria de las dependencias del servicio. Ademas de responder,
// llevan la cuenta de las llamadas: varias reglas de este servicio (que una
// lectura cacheada no toque la BD, que una escritura invalide) solo se pueden
// afirmar mirando a quien se llamo y a quien no.

const (
	testUserID   = "11111111-1111-1111-1111-111111111111"
	testEmail    = "autor@lectonautas.dev"
	testUsername = "autor"
	testPassword = "password123"
)

// --- Repositorio de usuarios ------------------------------------------------

type fakeUserRepo struct {
	users map[string]*domain.User
	// byEmail guarda el usuario con su hash, como GetByEmailForAuth.
	byEmail map[string]*domain.User

	createErr error
	updateErr error
	deleteErr error

	// Contadores para afirmar que el cache evito el viaje a la BD.
	getByIDCalls int
	getAllCalls  int

	// created guarda lo ultimo que se intento insertar, para poder inspeccionar
	// que la password llego hasheada y no en claro.
	created *domain.User
}

func newFakeUserRepo() *fakeUserRepo {
	return &fakeUserRepo{
		users:   map[string]*domain.User{},
		byEmail: map[string]*domain.User{},
	}
}

func (r *fakeUserRepo) Create(_ context.Context, u *domain.User) (*domain.User, error) {
	r.created = u
	if r.createErr != nil {
		return nil, r.createErr
	}
	u.ID = testUserID
	u.IsActive = true
	u.CreatedAt = time.Now()
	u.UpdatedAt = u.CreatedAt
	r.users[u.ID] = u
	r.byEmail[u.Email] = u
	return u, nil
}

func (r *fakeUserRepo) GetByID(_ context.Context, id string) (*domain.User, error) {
	r.getByIDCalls++
	u, ok := r.users[id]
	if !ok {
		return nil, repository.ErrUserNotFound
	}
	return u, nil
}

func (r *fakeUserRepo) GetByEmailForAuth(_ context.Context, email string) (*domain.User, error) {
	u, ok := r.byEmail[email]
	if !ok {
		return nil, repository.ErrUserNotFound
	}
	return u, nil
}

func (r *fakeUserRepo) Update(_ context.Context, upd *domain.UserUpdate) (*domain.User, error) {
	if r.updateErr != nil {
		return nil, r.updateErr
	}
	u, ok := r.users[upd.ID]
	if !ok {
		return nil, repository.ErrUserNotFound
	}
	if upd.Username != nil {
		u.Username = *upd.Username
	}
	if upd.IsActive != nil {
		u.IsActive = *upd.IsActive
	}
	return u, nil
}

func (r *fakeUserRepo) Delete(_ context.Context, id string) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	if _, ok := r.users[id]; !ok {
		return repository.ErrUserNotFound
	}
	delete(r.users, id)
	return nil
}

func (r *fakeUserRepo) GetAll(_ context.Context) ([]*domain.User, error) {
	r.getAllCalls++
	out := make([]*domain.User, 0, len(r.users))
	for _, u := range r.users {
		out = append(out, u)
	}
	return out, nil
}

// --- Repositorio de sesiones ------------------------------------------------

type fakeSessionRepo struct {
	// created guarda la sesion insertada: sirve para afirmar que lo que se
	// persiste es el hash del token y nunca el token en crudo.
	created *domain.Session
	valid   *domain.Session

	createErr error
	getErr    error
	revokeErr error

	getCalls int
}

func (r *fakeSessionRepo) Create(_ context.Context, s *domain.Session) error {
	if r.createErr != nil {
		return r.createErr
	}
	r.created = s
	return nil
}

func (r *fakeSessionRepo) GetValidByTokenHash(_ context.Context, hash string) (*domain.Session, error) {
	r.getCalls++
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.valid == nil || r.valid.TokenHash != hash {
		return nil, repository.ErrSessionNotFound
	}
	return r.valid, nil
}

func (r *fakeSessionRepo) Revoke(context.Context, string) error { return r.revokeErr }

// --- Caches -----------------------------------------------------------------

type fakeSessionCache struct {
	entries map[string]string
	sets    int
	deletes int
}

func newFakeSessionCache() *fakeSessionCache {
	return &fakeSessionCache{entries: map[string]string{}}
}

func (c *fakeSessionCache) Set(_ context.Context, hash, userID string, _ time.Duration) error {
	c.sets++
	c.entries[hash] = userID
	return nil
}

func (c *fakeSessionCache) Get(_ context.Context, hash string) (string, error) {
	userID, ok := c.entries[hash]
	if !ok {
		return "", cache.ErrMiss
	}
	return userID, nil
}

func (c *fakeSessionCache) Delete(_ context.Context, hash string) error {
	c.deletes++
	delete(c.entries, hash)
	return nil
}

type fakeUserCache struct {
	users map[string]*domain.User
	all   []*domain.User

	invalidatedUsers []string
	invalidatedAll   int
}

func newFakeUserCache() *fakeUserCache {
	return &fakeUserCache{users: map[string]*domain.User{}}
}

func (c *fakeUserCache) SetUser(_ context.Context, u *domain.User, _ time.Duration) error {
	c.users[u.ID] = u
	return nil
}

func (c *fakeUserCache) GetUser(_ context.Context, id string) (*domain.User, error) {
	u, ok := c.users[id]
	if !ok {
		return nil, cache.ErrMiss
	}
	return u, nil
}

func (c *fakeUserCache) SetAllUsers(_ context.Context, users []*domain.User, _ time.Duration) error {
	c.all = users
	return nil
}

func (c *fakeUserCache) GetAllUsers(context.Context) ([]*domain.User, error) {
	if c.all == nil {
		return nil, cache.ErrMiss
	}
	return c.all, nil
}

func (c *fakeUserCache) InvalidateUser(_ context.Context, id string) error {
	c.invalidatedUsers = append(c.invalidatedUsers, id)
	delete(c.users, id)
	c.all = nil
	return nil
}

func (c *fakeUserCache) InvalidateAllUsers(context.Context) error {
	c.invalidatedAll++
	c.all = nil
	return nil
}

// --- Armado -----------------------------------------------------------------

// harness junta el servicio con sus dobles para que las pruebas puedan
// inspeccionarlos despues de la llamada.
type harness struct {
	svc          *UserService
	repo         *fakeUserRepo
	sessions     *fakeSessionRepo
	sessionCache *fakeSessionCache
	userCache    *fakeUserCache
}

func newHarness() *harness {
	repo := newFakeUserRepo()
	sessions := &fakeSessionRepo{}
	sessionCache := newFakeSessionCache()
	userCache := newFakeUserCache()

	return &harness{
		svc:          NewUserService(repo, sessions, sessionCache, userCache),
		repo:         repo,
		sessions:     sessions,
		sessionCache: sessionCache,
		userCache:    userCache,
	}
}

// conUsuario deja un usuario ya existente, con la password hasheada como lo
// haria CreateUser.
func (h *harness) conUsuario(t interface{ Fatalf(string, ...any) }, activo bool) *domain.User {
	u := &domain.User{
		ID:        testUserID,
		Email:     testEmail,
		Username:  testUsername,
		Password:  hashDePrueba(t, testPassword),
		IsActive:  activo,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	h.repo.users[u.ID] = u
	h.repo.byEmail[u.Email] = u
	return u
}
