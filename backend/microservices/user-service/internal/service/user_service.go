package service

import (
	"context"
	"errors"
	"log"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/cache"
	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/domain"
	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/repository"
	userv1 "github.com/EstebanTech/lectonautas/backend/microservices/user-service/proto/user/v1"
)

// El username es el handle publico, asi que se mantiene restringido: solo
// minusculas, digitos, guion y guion bajo.
var usernamePattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

// userCacheTTL es cuanto viven en Valkey los datos de usuario que devuelven los
// GET. Es corto a proposito: las escrituras que pasan por la API invalidan la
// clave al instante, pero este TTL es el techo de cuanto puede quedar viejo un
// dato que cambio por fuera (escritura directa a la BD, otro servicio).
const userCacheTTL = 15 * time.Minute

const (
	usernameMinLen    = 3
	usernameMaxLen    = 30
	passwordMinLen    = 8
	passwordMaxLen    = 72 // limite de bcrypt
	displayNameMaxLen = 100
	avatarURLMaxLen   = 500
	bioMaxLen         = 1000
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

// Las implementaciones reales tienen que seguir cumpliendo el contrato.
var (
	_ SessionCache = (*cache.SessionCache)(nil)
	_ UserCache    = (*cache.UserCache)(nil)
)

type UserService struct {
	userv1.UnimplementedUserServiceServer
	repo      repository.UserRepository
	sessions  repository.SessionRepository
	cache     SessionCache
	userCache UserCache
	content   ContentDeleter
}

func NewUserService(
	repo repository.UserRepository,
	sessions repository.SessionRepository,
	sessionCache SessionCache,
	userCache UserCache,
	content ContentDeleter,
) *UserService {
	return &UserService{
		repo:      repo,
		sessions:  sessions,
		cache:     sessionCache,
		userCache: userCache,
		content:   content,
	}
}

func (s *UserService) CreateUser(ctx context.Context, req *userv1.CreateUserRequest) (*userv1.UserResponse, error) {
	email, err := normalizeEmail(req.GetEmail())
	if err != nil {
		return nil, err
	}

	username, err := normalizeUsername(req.GetUsername())
	if err != nil {
		return nil, err
	}

	if err := validatePassword(req.GetPassword()); err != nil {
		return nil, err
	}

	displayName, err := optionalText("display_name", req.GetDisplayName(), displayNameMaxLen)
	if err != nil {
		return nil, err
	}
	avatarURL, err := optionalText("avatar_url", req.GetAvatarUrl(), avatarURLMaxLen)
	if err != nil {
		return nil, err
	}
	bio, err := optionalText("bio", req.GetBio(), bioMaxLen)
	if err != nil {
		return nil, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.GetPassword()), bcrypt.DefaultCost)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to hash password")
	}

	u := &domain.User{
		Email:       email,
		Username:    username,
		Password:    string(hash),
		DisplayName: displayName,
		AvatarURL:   avatarURL,
		Bio:         bio,
	}

	created, err := s.repo.Create(ctx, u)
	if err != nil {
		return nil, mapRepoErr(err, "failed to create user")
	}

	// El listado completo quedo viejo: le falta este usuario.
	if err := s.userCache.InvalidateAllUsers(ctx); err != nil {
		log.Printf("all users cache invalidate failed: %v", err)
	}

	return &userv1.UserResponse{User: toProto(created)}, nil
}

// GetUser devuelve el perfil publico. No pide token: el id de un autor viaja en
// cada libro, asi que esto es de acceso publico por diseno, y justo por eso lo
// que devuelve no puede incluir el email ni el estado de la cuenta. Los datos
// propios completos salen por GetCurrentUser.
func (s *UserService) GetUser(ctx context.Context, req *userv1.GetUserRequest) (*userv1.PublicUserResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	u, err := s.userByID(ctx, req.GetId())
	if err != nil {
		return nil, err
	}

	return &userv1.PublicUserResponse{User: toPublicProto(u)}, nil
}

// userByID resuelve un usuario con cache-aside: primero Valkey, y si hay miss
// va a la BD y repuebla la clave. Lo comparten GetUser y GetCurrentUser, asi
// que ambos aprovechan la misma entrada.
func (s *UserService) userByID(ctx context.Context, id string) (*domain.User, error) {
	u, err := s.userCache.GetUser(ctx, id)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, cache.ErrMiss) {
		// Error real de Valkey (no un simple miss): seguimos con la BD igual.
		log.Printf("user cache get failed: %v", err)
	}

	u, err = s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err, "failed to get user")
	}

	if err := s.userCache.SetUser(ctx, u, userCacheTTL); err != nil {
		// No es fatal: el usuario ya se resolvio desde la BD.
		log.Printf("user cache set failed: %v", err)
	}
	return u, nil
}

func (s *UserService) UpdateUser(ctx context.Context, req *userv1.UpdateUserRequest) (*userv1.UserResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	// Una cuenta solo la edita su dueno. El id sigue en la ruta por comodidad
	// del cliente, pero quien manda es el token: sin este chequeo bastaba con
	// conocer un id ajeno (y los ids de autor son publicos, van en cada libro)
	// para reescribir el perfil de cualquiera.
	if err := s.requireSelf(ctx, req.GetId()); err != nil {
		return nil, err
	}

	upd := &domain.UserUpdate{ID: req.GetId()}

	if req.Username != nil {
		username, err := normalizeUsername(req.GetUsername())
		if err != nil {
			return nil, err
		}
		upd.Username = &username
	}
	if req.DisplayName != nil {
		v, err := boundedText("display_name", req.GetDisplayName(), displayNameMaxLen)
		if err != nil {
			return nil, err
		}
		upd.DisplayName = &v
	}
	if req.AvatarUrl != nil {
		v, err := boundedText("avatar_url", req.GetAvatarUrl(), avatarURLMaxLen)
		if err != nil {
			return nil, err
		}
		upd.AvatarURL = &v
	}
	if req.Bio != nil {
		v, err := boundedText("bio", req.GetBio(), bioMaxLen)
		if err != nil {
			return nil, err
		}
		upd.Bio = &v
	}
	if req.IsActive != nil {
		v := req.GetIsActive()
		upd.IsActive = &v
	}

	if upd.Username == nil && upd.DisplayName == nil && upd.AvatarURL == nil && upd.Bio == nil && upd.IsActive == nil {
		return nil, status.Error(codes.InvalidArgument, "no fields to update")
	}

	updated, err := s.repo.Update(ctx, upd)
	if err != nil {
		return nil, mapRepoErr(err, "failed to update user")
	}

	// Se invalida en vez de reescribir la clave: si el Set fallara, quedaria
	// sirviendo el usuario viejo hasta que venza el TTL.
	if err := s.userCache.InvalidateUser(ctx, updated.ID); err != nil {
		log.Printf("user cache invalidate failed: %v", err)
	}

	return &userv1.UserResponse{User: toProto(updated)}, nil
}

// DeleteUser da de baja la cuenta del llamante y, con ella, todo lo que colgaba
// del usuario: sus sesiones (CASCADE en esta misma base) y su contenido en
// library-service, que se borra antes por gRPC.
//
// El contenido va primero a proposito. Si fallara, la cuenta sigue en pie y el
// cliente puede reintentar; al reves quedaria obra sin dueno y sin forma de
// llegar a ella, porque el author_id apunta a un usuario que ya no existe.
func (s *UserService) DeleteUser(ctx context.Context, req *userv1.DeleteUserRequest) (*userv1.DeleteUserResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	// Solo el dueno puede darse de baja: es la operacion mas destructiva de la
	// API y con la cascada se lleva por delante toda su obra.
	if err := s.requireSelf(ctx, req.GetId()); err != nil {
		return nil, err
	}

	if err := s.content.DeleteAuthorContent(ctx, req.GetId()); err != nil {
		log.Printf("delete author content failed: %v", err)
		return nil, status.Error(codes.Internal, "failed to delete the account content")
	}

	if err := s.repo.Delete(ctx, req.GetId()); err != nil {
		return nil, mapRepoErr(err, "failed to delete user")
	}

	if err := s.userCache.InvalidateUser(ctx, req.GetId()); err != nil {
		log.Printf("user cache invalidate failed: %v", err)
	}
	// El listado completo quedo viejo: le sobra este usuario.
	if err := s.userCache.InvalidateAllUsers(ctx); err != nil {
		log.Printf("all users cache invalidate failed: %v", err)
	}

	return &userv1.DeleteUserResponse{Success: true}, nil
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

// GetAllUsers devuelve todos los usuarios, sin paginacion ni filtros. Tambien
// va por cache-aside: la respuesta completa vive en una sola clave de Valkey.
func (s *UserService) GetAllUsers(ctx context.Context, _ *userv1.GetAllUsersRequest) (*userv1.GetAllUsersResponse, error) {
	users, err := s.userCache.GetAllUsers(ctx)
	if err != nil {
		if !errors.Is(err, cache.ErrMiss) {
			log.Printf("all users cache get failed: %v", err)
		}

		users, err = s.repo.GetAll(ctx)
		if err != nil {
			return nil, status.Error(codes.Internal, "failed to get users")
		}

		if err := s.userCache.SetAllUsers(ctx, users, userCacheTTL); err != nil {
			log.Printf("all users cache set failed: %v", err)
		}
	}

	// Perfiles publicos: este listado tampoco pide token.
	out := make([]*userv1.PublicUser, 0, len(users))
	for _, u := range users {
		out = append(out, toPublicProto(u))
	}

	return &userv1.GetAllUsersResponse{Users: out, Total: int32(len(out))}, nil
}

func normalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" {
		return "", status.Error(codes.InvalidArgument, "email is required")
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return "", status.Error(codes.InvalidArgument, "email is not a valid address")
	}
	return email, nil
}

func normalizeUsername(raw string) (string, error) {
	username := strings.ToLower(strings.TrimSpace(raw))
	if username == "" {
		return "", status.Error(codes.InvalidArgument, "username is required")
	}
	if len(username) < usernameMinLen || len(username) > usernameMaxLen {
		return "", status.Errorf(codes.InvalidArgument, "username must be between %d and %d characters", usernameMinLen, usernameMaxLen)
	}
	if !usernamePattern.MatchString(username) {
		return "", status.Error(codes.InvalidArgument, "username may only contain lowercase letters, digits, hyphens and underscores")
	}
	return username, nil
}

func validatePassword(password string) error {
	if password == "" {
		return status.Error(codes.InvalidArgument, "password is required")
	}
	if len(password) < passwordMinLen || len(password) > passwordMaxLen {
		return status.Errorf(codes.InvalidArgument, "password must be between %d and %d characters", passwordMinLen, passwordMaxLen)
	}
	return nil
}

// boundedText recorta y valida la longitud de un campo opcional; la cadena
// vacia es valida y significa "sin valor".
func boundedText(field, raw string, maxLen int) (string, error) {
	v := strings.TrimSpace(raw)
	if len(v) > maxLen {
		return "", status.Errorf(codes.InvalidArgument, "%s must be at most %d characters", field, maxLen)
	}
	return v, nil
}

// optionalText es boundedText devolviendo nil cuando el campo viene vacio, para
// escribir NULL en vez de una cadena vacia.
func optionalText(field, raw string, maxLen int) (*string, error) {
	v, err := boundedText(field, raw, maxLen)
	if err != nil {
		return nil, err
	}
	if v == "" {
		return nil, nil
	}
	return &v, nil
}

func mapRepoErr(err error, fallback string) error {
	switch {
	case errors.Is(err, repository.ErrUserNotFound):
		return status.Error(codes.NotFound, "user not found")
	case errors.Is(err, repository.ErrEmailTaken):
		return status.Error(codes.AlreadyExists, "email already registered")
	case errors.Is(err, repository.ErrUsernameTaken):
		return status.Error(codes.AlreadyExists, "username already taken")
	default:
		return status.Error(codes.Internal, fallback)
	}
}

// toProto arma la vista completa, con email. Solo puede usarse en respuestas
// que van hacia el propio dueno de la cuenta: CreateUser, Login, GetCurrentUser
// y UpdateUser. Todo lo demas usa toPublicProto.
func toProto(u *domain.User) *userv1.User {
	return &userv1.User{
		Id:          u.ID,
		Email:       u.Email,
		Username:    u.Username,
		DisplayName: deref(u.DisplayName),
		AvatarUrl:   deref(u.AvatarURL),
		Bio:         deref(u.Bio),
		IsActive:    u.IsActive,
		CreatedAt:   timestamppb.New(u.CreatedAt),
		UpdatedAt:   timestamppb.New(u.UpdatedAt),
	}
}

// toPublicProto arma la vista que puede ver cualquiera. El tipo de destino no
// tiene campo email, asi que aqui no hay nada que recordar omitir.
func toPublicProto(u *domain.User) *userv1.PublicUser {
	return &userv1.PublicUser{
		Id:          u.ID,
		Username:    u.Username,
		DisplayName: deref(u.DisplayName),
		AvatarUrl:   deref(u.AvatarURL),
		Bio:         deref(u.Bio),
		CreatedAt:   timestamppb.New(u.CreatedAt),
	}
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
