package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/domain"
	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/repository"
	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/token"
	userv1 "github.com/EstebanTech/lectonautas/backend/microservices/user-service/proto/user/v1"
	sharedcache "github.com/EstebanTech/lectonautas/backend/shared/cache"
	"github.com/EstebanTech/lectonautas/backend/shared/logx"
)

const (
	// sessionTTL es cuanto vive la sesion como tal: un ano en la BD
	// (expires_at). Es la ventana real en la que el token sigue siendo valido.
	sessionTTL = 365 * 24 * time.Hour

	// sessionCacheTTL es cuanto vive la entrada de sesion en Valkey. Es solo
	// cache: cuando expira, el siguiente acceso cae a la BD y repuebla. No
	// acorta la sesion ni cambia lo que hay en Postgres. Es mas largo que el
	// TTL de los datos de usuario porque una sesion no cambia sola: solo la
	// mueve un logout, y ese ya borra la clave explicitamente.
	sessionCacheTTL = 2 * time.Hour
)

// sessionCacheTTLFor acota el TTL de cache al tiempo que le quede a la sesion,
// para no dejar en Valkey una entrada que sobreviva a su propio expires_at.
func sessionCacheTTLFor(expiresAt time.Time) time.Duration {
	remaining := time.Until(expiresAt)
	if remaining < sessionCacheTTL {
		return remaining
	}
	return sessionCacheTTL
}

// Login verifica email + password y, si son validos, crea una sesion: genera un
// token aleatorio, guarda su hash en la BD (vigente por sessionTTL) y lo cachea
// en Valkey (sessionCacheTTL), y devuelve el token en crudo (unica vez que el
// cliente lo ve).
func (s *UserService) Login(ctx context.Context, req *userv1.LoginRequest) (*userv1.LoginResponse, error) {
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
	if err := s.cache.Set(ctx, hash, user.ID, sessionCacheTTLFor(sess.ExpiresAt)); err != nil {
		// No es fatal: la sesion ya vive en la BD y se repuebla en el proximo acceso.
		logx.From(ctx).Warn("session cache set failed", slog.String("error", err.Error()))
	}

	user.Password = ""
	return &userv1.LoginResponse{Token: raw, User: toProto(user)}, nil
}

// GetCurrentUser devuelve el usuario dueno del token del header Authorization.
func (s *UserService) GetCurrentUser(ctx context.Context, _ *userv1.GetCurrentUserRequest) (*userv1.UserResponse, error) {
	userID, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}

	u, err := s.userByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &userv1.UserResponse{User: toProto(u)}, nil
}

// Logout revoca la sesion del token del header Authorization (en la BD) y la
// borra de Valkey.
func (s *UserService) Logout(ctx context.Context, _ *userv1.LogoutRequest) (*userv1.LogoutResponse, error) {
	raw, err := bearerToken(ctx)
	if err != nil {
		return nil, err
	}
	hash := token.Hash(raw)

	if err := s.cache.Delete(ctx, hash); err != nil {
		logx.From(ctx).Warn("session cache delete failed", slog.String("error", err.Error()))
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
		logx.From(ctx).Error("logout revoke failed", slog.String("error", err.Error()))
		return nil, status.Error(codes.Internal, "failed to logout")
	}
	return &userv1.LogoutResponse{Success: true}, nil
}

// ValidateSession resuelve un token a su user_id para otros servicios. No lo
// expone el gateway (no tiene anotacion HTTP): lo llaman los vecinos por gRPC,
// que no tienen la tabla de sesiones y no deben tenerla.
func (s *UserService) ValidateSession(ctx context.Context, req *userv1.ValidateSessionRequest) (*userv1.ValidateSessionResponse, error) {
	raw := strings.TrimSpace(req.GetToken())
	if raw == "" {
		return nil, status.Error(codes.InvalidArgument, "token is required")
	}

	userID, err := s.resolveSession(ctx, raw)
	if err != nil {
		return nil, err
	}
	return &userv1.ValidateSessionResponse{UserId: userID}, nil
}

// authenticate resuelve la sesion del token que viene en el header Authorization.
func (s *UserService) authenticate(ctx context.Context) (string, error) {
	raw, err := bearerToken(ctx)
	if err != nil {
		return "", err
	}
	return s.resolveSession(ctx, raw)
}

// resolveSession es el flujo cache-aside: hashea el token, lo busca en Valkey;
// si hay miss, cae a la BD y, si la sesion sigue vigente, repuebla Valkey.
// Devuelve el user_id de la sesion.
//
// Es el unico punto donde se resuelve una sesion, lo llame el propio cliente
// (por el header) o un servicio vecino (por ValidateSession). Los vecinos no
// leen esta clave de Valkey directamente: tendrian que replicar el hasheo del
// token y, ante un miss, no tendrian a que caer.
func (s *UserService) resolveSession(ctx context.Context, raw string) (string, error) {
	hash := token.Hash(raw)

	// 1) Valkey primero.
	userID, err := s.cache.Get(ctx, hash)
	if err == nil {
		return userID, nil
	}
	if !errors.Is(err, sharedcache.ErrMiss) {
		// Error real de Valkey (no un simple miss): seguimos con la BD igual.
		logx.From(ctx).Warn("session cache get failed", slog.String("error", err.Error()))
	}

	// 2) Miss: la fuente de verdad es la BD.
	sess, err := s.sessions.GetValidByTokenHash(ctx, hash)
	if err != nil {
		if errors.Is(err, repository.ErrSessionNotFound) {
			return "", status.Error(codes.Unauthenticated, "invalid or expired token")
		}
		return "", status.Error(codes.Internal, "failed to validate session")
	}

	// 3) Repoblar Valkey por otra ventana de cache (o menos, si a la sesion le
	// queda menos que eso).
	if ttl := sessionCacheTTLFor(sess.ExpiresAt); ttl > 0 {
		if err := s.cache.Set(ctx, hash, sess.UserID, ttl); err != nil {
			logx.From(ctx).Warn("session cache set failed", slog.String("error", err.Error()))
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
