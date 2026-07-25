package service

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/estebandeveloper20/lectonautas/backend/microservices/user-service/internal/cache"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/user-service/internal/domain"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/user-service/internal/repository"
	userv1 "github.com/estebandeveloper20/lectonautas/backend/microservices/user-service/proto/user/v1"
)

// stubSessionRepo permite forzar el error que devuelve Revoke sin tocar la BD.
type stubSessionRepo struct {
	revokeErr error
}

func (s *stubSessionRepo) Create(context.Context, *domain.Session) error { return nil }
func (s *stubSessionRepo) GetValidByTokenHash(context.Context, string) (*domain.Session, error) {
	return nil, nil
}
func (s *stubSessionRepo) Revoke(context.Context, string) error { return s.revokeErr }

// ctxConToken mete un header Authorization en el contexto entrante, como haria
// Envoy al reenviar la peticion al servicio.
func ctxConToken() context.Context {
	base := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("authorization", "Bearer token-de-prueba"),
	)
	// Timeout corto para acotar el intento de conexion a Valkey (que en el test
	// no existe): el error se ignora en Logout, pero no queremos que bloquee.
	ctx, _ := context.WithTimeout(base, 2*time.Second)
	return ctx
}

// Valkey no existe en el test; el puerto 1 da "connection refused" al instante y
// el Logout ignora ese error de cache, que es justo lo que queremos ejercitar.
func newTestService(sessions repository.SessionRepository) *UserService {
	valkey := cache.NewClient("127.0.0.1:1")
	return NewUserService(nil, sessions, cache.NewSessionCache(valkey), cache.NewUserCache(valkey))
}

// Caso del reporte: token sin sesion activa. Debe ser Unauthenticated (401 en el
// gateway), NUNCA Internal (500).
func TestLogout_SessionAlreadyClosed(t *testing.T) {
	svc := newTestService(&stubSessionRepo{revokeErr: repository.ErrSessionNotFound})

	_, err := svc.Logout(ctxConToken(), &userv1.LogoutRequest{})

	if code := status.Code(err); code != codes.Unauthenticated {
		t.Fatalf("esperaba Unauthenticated, obtuve %s (err=%v)", code, err)
	}
}

// Un error real de la BD (no ErrSessionNotFound) si debe ser Internal (500).
func TestLogout_DBError(t *testing.T) {
	svc := newTestService(&stubSessionRepo{revokeErr: context.DeadlineExceeded})

	_, err := svc.Logout(ctxConToken(), &userv1.LogoutRequest{})

	if code := status.Code(err); code != codes.Internal {
		t.Fatalf("esperaba Internal, obtuve %s (err=%v)", code, err)
	}
}

// Revoke exitoso: logout normal, sin error.
func TestLogout_Success(t *testing.T) {
	svc := newTestService(&stubSessionRepo{revokeErr: nil})

	resp, err := svc.Logout(ctxConToken(), &userv1.LogoutRequest{})

	if err != nil {
		t.Fatalf("no esperaba error, obtuve %v", err)
	}
	if !resp.GetSuccess() {
		t.Fatal("esperaba success=true")
	}
}
