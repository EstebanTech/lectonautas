package service

import (
	"context"
	"errors"
	"net/mail"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/estebandeveloper20/lectonautas/backend/microservices/users-service/internal/cache"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/users-service/internal/domain"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/users-service/internal/repository"
	usersv1 "github.com/estebandeveloper20/lectonautas/backend/microservices/users-service/proto/users/v1"
)

// El username es el handle publico, asi que se mantiene restringido: solo
// minusculas, digitos, guion y guion bajo.
var usernamePattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

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
	usersv1.UnimplementedUsersServiceServer
	repo     repository.UserRepository
	sessions repository.SessionRepository
	cache    *cache.SessionCache
}

func NewUserService(repo repository.UserRepository, sessions repository.SessionRepository, sessionCache *cache.SessionCache) *UserService {
	return &UserService{repo: repo, sessions: sessions, cache: sessionCache}
}

func (s *UserService) CreateUser(ctx context.Context, req *usersv1.CreateUserRequest) (*usersv1.UserResponse, error) {
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

	return &usersv1.UserResponse{User: toProto(created)}, nil
}

func (s *UserService) GetUser(ctx context.Context, req *usersv1.GetUserRequest) (*usersv1.UserResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	u, err := s.repo.GetByID(ctx, req.GetId())
	if err != nil {
		return nil, mapRepoErr(err, "failed to get user")
	}

	return &usersv1.UserResponse{User: toProto(u)}, nil
}

func (s *UserService) UpdateUser(ctx context.Context, req *usersv1.UpdateUserRequest) (*usersv1.UserResponse, error) {
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

	return &usersv1.UserResponse{User: toProto(updated)}, nil
}

func (s *UserService) DeleteUser(ctx context.Context, req *usersv1.DeleteUserRequest) (*usersv1.DeleteUserResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	if err := s.repo.Delete(ctx, req.GetId()); err != nil {
		return nil, mapRepoErr(err, "failed to delete user")
	}

	return &usersv1.DeleteUserResponse{Success: true}, nil
}

func (s *UserService) ListUsers(ctx context.Context, req *usersv1.ListUsersRequest) (*usersv1.ListUsersResponse, error) {
	page := req.GetPage()
	pageSize := req.GetPageSize()
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	users, total, err := s.repo.List(ctx, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to list users")
	}

	out := make([]*usersv1.User, 0, len(users))
	for _, u := range users {
		out = append(out, toProto(u))
	}

	return &usersv1.ListUsersResponse{Users: out, Total: total}, nil
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

func toProto(u *domain.User) *usersv1.User {
	return &usersv1.User{
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
