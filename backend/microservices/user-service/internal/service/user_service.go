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

	"github.com/estebandeveloper20/lectonautas/backend/microservices/user-service/internal/cache"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/user-service/internal/domain"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/user-service/internal/repository"
	userv1 "github.com/estebandeveloper20/lectonautas/backend/microservices/user-service/proto/user/v1"
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

type UserService struct {
	userv1.UnimplementedUserServiceServer
	repo      repository.UserRepository
	sessions  repository.SessionRepository
	cache     *cache.SessionCache
	userCache *cache.UserCache
}

func NewUserService(
	repo repository.UserRepository,
	sessions repository.SessionRepository,
	sessionCache *cache.SessionCache,
	userCache *cache.UserCache,
) *UserService {
	return &UserService{repo: repo, sessions: sessions, cache: sessionCache, userCache: userCache}
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

func (s *UserService) GetUser(ctx context.Context, req *userv1.GetUserRequest) (*userv1.UserResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	u, err := s.userByID(ctx, req.GetId())
	if err != nil {
		return nil, err
	}

	return &userv1.UserResponse{User: toProto(u)}, nil
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

func (s *UserService) DeleteUser(ctx context.Context, req *userv1.DeleteUserRequest) (*userv1.DeleteUserResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	if err := s.repo.Delete(ctx, req.GetId()); err != nil {
		return nil, mapRepoErr(err, "failed to delete user")
	}

	if err := s.userCache.InvalidateUser(ctx, req.GetId()); err != nil {
		log.Printf("user cache invalidate failed: %v", err)
	}

	return &userv1.DeleteUserResponse{Success: true}, nil
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

	out := make([]*userv1.User, 0, len(users))
	for _, u := range users {
		out = append(out, toProto(u))
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

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
