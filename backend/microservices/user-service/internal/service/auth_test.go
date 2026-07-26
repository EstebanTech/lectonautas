package service

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"

	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/repository"
	userv1 "github.com/EstebanTech/lectonautas/backend/microservices/user-service/proto/user/v1"
)

// Pruebas del logout. Antes armaban el servicio con un cliente de Valkey
// apuntando a un puerto muerto para provocar el error de cache que Logout
// ignora; ahora eso lo cubre el doble, sin esperar a que la conexion falle.

func logoutConRevoke(revokeErr error) *harness {
	h := newHarness()
	h.sessions.revokeErr = revokeErr
	return h
}

// Caso del reporte: token sin sesion activa. Debe ser Unauthenticated (401 en el
// gateway), NUNCA Internal (500).
func TestLogout_SessionAlreadyClosed(t *testing.T) {
	h := logoutConRevoke(repository.ErrSessionNotFound)

	_, err := h.svc.Logout(ctxConHeader("Bearer token-de-prueba"), &userv1.LogoutRequest{})

	requireCode(t, err, codes.Unauthenticated)
}

// Un error real de la BD (no ErrSessionNotFound) si debe ser Internal (500).
func TestLogout_DBError(t *testing.T) {
	h := logoutConRevoke(context.DeadlineExceeded)

	_, err := h.svc.Logout(ctxConHeader("Bearer token-de-prueba"), &userv1.LogoutRequest{})

	requireCode(t, err, codes.Internal)
}

// Revoke exitoso: logout normal, sin error.
func TestLogout_Success(t *testing.T) {
	h := logoutConRevoke(nil)

	resp, err := h.svc.Logout(ctxConHeader("Bearer token-de-prueba"), &userv1.LogoutRequest{})

	if err != nil {
		t.Fatalf("no esperaba error, obtuve %v", err)
	}
	if !resp.GetSuccess() {
		t.Fatal("esperaba success=true")
	}
}

// El logout tiene que sacar la sesion de Valkey ademas de revocarla en la BD:
// si solo se revocara, el token seguiria resolviendo desde el cache hasta que
// venciera su TTL.
func TestLogout_BorraLaSesionDelCache(t *testing.T) {
	h := logoutConRevoke(nil)

	if _, err := h.svc.Logout(ctxConHeader("Bearer token-de-prueba"), &userv1.LogoutRequest{}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if h.sessionCache.deletes != 1 {
		t.Fatalf("borrados del cache de sesion = %d, se esperaba 1", h.sessionCache.deletes)
	}
}

func TestLogout_SinToken(t *testing.T) {
	h := newHarness()

	_, err := h.svc.Logout(context.Background(), &userv1.LogoutRequest{})

	requireCode(t, err, codes.Unauthenticated)
}
