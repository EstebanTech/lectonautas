package service

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/estebandeveloper20/lectonautas/backend/microservices/users-service/internal/cache"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/users-service/internal/domain"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/users-service/internal/repository"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/users-service/internal/token"
	usersv1 "github.com/estebandeveloper20/lectonautas/backend/microservices/users-service/proto/users/v1"
)

// sessionTTL es cuanto vive una sesion: 2h en Valkey y la misma ventana en la
// BD (expires_at).
const sessionTTL = 2 * time.Hour

// Login verifica email + password y, si son validos, crea una sesion: genera un
// token aleatorio, guarda su hash en la BD y en Valkey (TTL 2h) y devuelve el
// token en crudo (unica vez que el cliente lo ve).
func (s *UserService) Login(ctx context.Context, req *usersv1.LoginRequest) (*usersv1.LoginResponse, error) {
	email, err := normalizeEmail(req.GetEmail())
	if err != nil {
		return nil, err
	}
	if req.GetPassword() == "" {
		return nil, status.Error(codes.InvalidArgument, "password is required")
	}

	user, err := s.repo.GetByEmailForAuth(ctx, email)
	if err != nil {
		if errors.Is(err, repository.ErrUserNotFound) {
			// Mismo error que password incorrecta: no revelar si el email existe.
			return nil, status.Error(codes.Unauthenticated, "invalid credentials")
		}
		return nil, status.Error(codes.Internal, "failed to login")
	}
	if !user.IsActive {
		return nil, status.Error(codes.PermissionDenied, "account is disabled")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.GetPassword())); err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}

	raw, err := token.New()
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to create session")
	}
	hash := token.Hash(raw)

	sess := &domain.Session{
		UserID:    user.ID,
		TokenHash: hash,
		UserAgent: userAgentFromCtx(ctx),
		ExpiresAt: time.Now().Add(sessionTTL),
	}
	if err := s.sessions.Create(ctx, sess); err != nil {
		return nil, status.Error(codes.Internal, "failed to create session")
	}
	if err := s.cache.Set(ctx, hash, user.ID, sessionTTL); err != nil {
		// No es fatal: la sesion ya vive en la BD y se repuebla en el proximo acceso.
		log.Printf("session cache set failed: %v", err)
	}

	user.Password = ""
	return &usersv1.LoginResponse{Token: raw, User: toProto(user)}, nil
}

// GetCurrentUser devuelve el usuario dueno del token del header Authorization.
func (s *UserService) GetCurrentUser(ctx context.Context, _ *usersv1.GetCurrentUserRequest) (*usersv1.UserResponse, error) {
	userID, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}

	u, err := s.repo.GetByID(ctx, userID)
	if err != nil {
		return nil, mapRepoErr(err, "failed to get user")
	}
	return &usersv1.UserResponse{User: toProto(u)}, nil
}

// Logout revoca la sesion del token del header Authorization (en la BD) y la
// borra de Valkey.
func (s *UserService) Logout(ctx context.Context, _ *usersv1.LogoutRequest) (*usersv1.LogoutResponse, error) {
	raw, err := bearerToken(ctx)
	if err != nil {
		return nil, err
	}
	hash := token.Hash(raw)

	if err := s.cache.Delete(ctx, hash); err != nil {
		log.Printf("session cache delete failed: %v", err)
	}
	if err := s.sessions.Revoke(ctx, hash); err != nil {
		if errors.Is(err, repository.ErrSessionNotFound) {
			// Revoke no toco ninguna fila: el token no existe o la sesion ya
			// estaba cerrada. En ambos casos no hay sesion activa que revocar,
			// asi que no es un logout exitoso.
			return nil, status.Error(codes.Unauthenticated, "session already closed")
		}
		// Error real de la BD: lo dejamos en el log (el cliente solo ve un
		// mensaje generico) para poder diagnosticar la causa.
		log.Printf("logout revoke failed: %v", err)
		return nil, status.Error(codes.Internal, "failed to logout")
	}
	return &usersv1.LogoutResponse{Success: true}, nil
}

// authenticate es el flujo cache-aside: hashea el token del header, lo busca en
// Valkey; si hay miss, cae a la BD y, si la sesion sigue vigente, repuebla
// Valkey con el TTL restante. Devuelve el user_id de la sesion.
func (s *UserService) authenticate(ctx context.Context) (string, error) {
	raw, err := bearerToken(ctx)
	if err != nil {
		return "", err
	}
	hash := token.Hash(raw)

	// 1) Valkey primero.
	userID, err := s.cache.Get(ctx, hash)
	if err == nil {
		return userID, nil
	}
	if !errors.Is(err, cache.ErrMiss) {
		// Error real de Valkey (no un simple miss): seguimos con la BD igual.
		log.Printf("session cache get failed: %v", err)
	}

	// 2) Miss: la fuente de verdad es la BD.
	sess, err := s.sessions.GetValidByTokenHash(ctx, hash)
	if err != nil {
		if errors.Is(err, repository.ErrSessionNotFound) {
			return "", status.Error(codes.Unauthenticated, "invalid or expired token")
		}
		return "", status.Error(codes.Internal, "failed to validate session")
	}

	// 3) Repoblar Valkey con el tiempo que le quede a la sesion.
	if ttl := time.Until(sess.ExpiresAt); ttl > 0 {
		if err := s.cache.Set(ctx, hash, sess.UserID, ttl); err != nil {
			log.Printf("session cache set failed: %v", err)
		}
	}
	return sess.UserID, nil
}

// bearerToken extrae el token en crudo del header "Authorization: Bearer <token>"
// (Envoy lo reenvia como metadata gRPC).
func bearerToken(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing authorization header")
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return "", status.Error(codes.Unauthenticated, "missing authorization header")
	}

	raw := strings.TrimSpace(values[0])
	// Aceptar "Bearer <token>" (cualquier capitalizacion) o el token pelado.
	if i := strings.IndexByte(raw, ' '); i != -1 && strings.EqualFold(raw[:i], "bearer") {
		raw = strings.TrimSpace(raw[i+1:])
	}
	if raw == "" {
		return "", status.Error(codes.Unauthenticated, "missing authorization header")
	}
	return raw, nil
}

func userAgentFromCtx(ctx context.Context) *string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil
	}
	values := md.Get("user-agent")
	if len(values) == 0 || values[0] == "" {
		return nil
	}
	v := values[0]
	if len(v) > 255 {
		v = v[:255]
	}
	return &v
}
