package service

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"

	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/domain"
	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/token"
	userv1 "github.com/EstebanTech/lectonautas/backend/microservices/user-service/proto/user/v1"
)

// ctxConHeader arma el contexto como llega desde Envoy, que reenvia el header
// Authorization como metadata gRPC.
func ctxConHeader(valor string) context.Context {
	return metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("authorization", valor),
	)
}

// --- Login ------------------------------------------------------------------

func TestLogin_ValidaLaEntrada(t *testing.T) {
	tests := []struct {
		nombre string
		req    *userv1.LoginRequest
	}{
		{"email vacio", &userv1.LoginRequest{Password: testPassword}},
		{"email invalido", &userv1.LoginRequest{Email: "noesuncorreo", Password: testPassword}},
		{"password vacia", &userv1.LoginRequest{Email: testEmail}},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			h := newHarness()
			_, err := h.svc.Login(context.Background(), tt.req)
			requireCode(t, err, codes.InvalidArgument)
		})
	}
}

// Un email que no existe y una password incorrecta tienen que devolver
// exactamente lo mismo. Si se distinguieran, la API serviria para averiguar que
// correos estan registrados.
func TestLogin_NoRevelaSiElEmailExiste(t *testing.T) {
	hInexistente := newHarness()
	_, errInexistente := hInexistente.svc.Login(context.Background(), &userv1.LoginRequest{
		Email: "nadie@lectonautas.dev", Password: testPassword,
	})

	hMalaPassword := newHarness()
	hMalaPassword.conUsuario(t, true)
	_, errMalaPassword := hMalaPassword.svc.Login(context.Background(), &userv1.LoginRequest{
		Email: testEmail, Password: "otra-password",
	})

	requireCode(t, errInexistente, codes.Unauthenticated)
	requireCode(t, errMalaPassword, codes.Unauthenticated)

	if errInexistente.Error() != errMalaPassword.Error() {
		t.Fatalf("los mensajes difieren y permiten enumerar correos:\n  inexistente: %v\n  password mala: %v",
			errInexistente, errMalaPassword)
	}
}

func TestLogin_CuentaDesactivada(t *testing.T) {
	h := newHarness()
	h.conUsuario(t, false)

	_, err := h.svc.Login(context.Background(), &userv1.LoginRequest{
		Email: testEmail, Password: testPassword,
	})

	// PermissionDenied y no Unauthenticated: las credenciales son correctas,
	// lo que pasa es que la cuenta esta deshabilitada.
	requireCode(t, err, codes.PermissionDenied)
}

func TestLogin_DevuelveElTokenEnCrudoYGuardaSoloElHash(t *testing.T) {
	h := newHarness()
	h.conUsuario(t, true)

	resp, err := h.svc.Login(context.Background(), &userv1.LoginRequest{
		Email: testEmail, Password: testPassword,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	crudo := resp.GetToken()
	if crudo == "" {
		t.Fatal("el login no devolvio token")
	}

	sesion := h.sessions.created
	if sesion == nil {
		t.Fatal("no se creo la sesion")
	}
	// Lo que se persiste nunca debe ser el token tal cual: quien lea la tabla
	// no puede quedarse con sesiones utilizables.
	if sesion.TokenHash == crudo {
		t.Fatal("se guardo el token en crudo en la sesion")
	}
	if sesion.TokenHash != token.Hash(crudo) {
		t.Fatal("lo guardado no es el hash del token devuelto")
	}
	if sesion.UserID != testUserID {
		t.Fatalf("user_id de la sesion = %q", sesion.UserID)
	}
	if !sesion.ExpiresAt.After(time.Now()) {
		t.Fatal("la sesion nacio ya vencida")
	}
}

func TestLogin_NuncaDevuelveLaPassword(t *testing.T) {
	h := newHarness()
	usuario := h.conUsuario(t, true)

	// El hash se captura ANTES: el servicio limpia el campo del dominio al
	// terminar, y compararlo despues seria comparar dos cadenas vacias.
	hash := usuario.Password

	resp, err := h.svc.Login(context.Background(), &userv1.LoginRequest{
		Email: testEmail, Password: testPassword,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	// El mensaje User no tiene campo password, pero se comprueba igual que
	// ningun campo de texto arrastre el hash.
	u := resp.GetUser()
	if u == nil {
		t.Fatal("el login no devolvio el usuario")
	}
	if hash == "" {
		t.Fatal("el usuario de prueba no tenia hash: la comprobacion no valdria")
	}
	for nombre, valor := range map[string]string{
		"email": u.GetEmail(), "username": u.GetUsername(),
		"display_name": u.GetDisplayName(), "bio": u.GetBio(), "avatar_url": u.GetAvatarUrl(),
	} {
		if valor == hash {
			t.Fatalf("el hash de la password se filtro en el campo %s", nombre)
		}
	}
}

func TestLogin_CacheaLaSesion(t *testing.T) {
	h := newHarness()
	h.conUsuario(t, true)

	if _, err := h.svc.Login(context.Background(), &userv1.LoginRequest{
		Email: testEmail, Password: testPassword,
	}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if h.sessionCache.sets != 1 {
		t.Fatalf("escrituras en el cache de sesion = %d, se esperaba 1", h.sessionCache.sets)
	}
}

// --- Resolucion del token ---------------------------------------------------

func TestGetCurrentUser_SinToken(t *testing.T) {
	h := newHarness()

	_, err := h.svc.GetCurrentUser(context.Background(), &userv1.GetCurrentUserRequest{})

	requireCode(t, err, codes.Unauthenticated)
}

func TestGetCurrentUser_TokenSinSesion(t *testing.T) {
	h := newHarness()
	h.conUsuario(t, true)

	_, err := h.svc.GetCurrentUser(ctxConHeader("Bearer token-que-no-existe"), &userv1.GetCurrentUserRequest{})

	requireCode(t, err, codes.Unauthenticated)
}

func TestGetCurrentUser_DevuelveElDuenoDelToken(t *testing.T) {
	h := newHarness()
	h.conUsuario(t, true)

	const crudo = "token-valido"
	h.sessions.valid = &domain.Session{
		UserID: testUserID, TokenHash: token.Hash(crudo), ExpiresAt: time.Now().Add(time.Hour),
	}

	resp, err := h.svc.GetCurrentUser(ctxConHeader("Bearer "+crudo), &userv1.GetCurrentUserRequest{})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resp.GetUser().GetId() != testUserID {
		t.Fatalf("id = %q, se esperaba %q", resp.GetUser().GetId(), testUserID)
	}
}

func TestBearerToken_AceptaLasFormasQueLlegan(t *testing.T) {
	const crudo = "token-valido"

	tests := []struct {
		nombre string
		header string
	}{
		{"Bearer estandar", "Bearer " + crudo},
		{"bearer en minuscula", "bearer " + crudo},
		{"BEARER en mayuscula", "BEARER " + crudo},
		{"el token pelado", crudo},
		{"con espacios alrededor", "  Bearer " + crudo + "  "},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			h := newHarness()
			h.conUsuario(t, true)
			h.sessions.valid = &domain.Session{
				UserID: testUserID, TokenHash: token.Hash(crudo), ExpiresAt: time.Now().Add(time.Hour),
			}

			_, err := h.svc.GetCurrentUser(ctxConHeader(tt.header), &userv1.GetCurrentUserRequest{})
			if err != nil {
				t.Fatalf("no se acepto el header %q: %v", tt.header, err)
			}
		})
	}
}

func TestResolveSession_ElHitDeCacheNoTocaLaBD(t *testing.T) {
	h := newHarness()
	h.conUsuario(t, true)

	const crudo = "token-valido"
	h.sessions.valid = &domain.Session{
		UserID: testUserID, TokenHash: token.Hash(crudo), ExpiresAt: time.Now().Add(time.Hour),
	}
	ctx := ctxConHeader("Bearer " + crudo)

	// Primera vez: miss, resuelve contra la BD y repuebla Valkey.
	if _, err := h.svc.GetCurrentUser(ctx, &userv1.GetCurrentUserRequest{}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if h.sessions.getCalls != 1 {
		t.Fatalf("consultas de sesion a la BD = %d, se esperaba 1", h.sessions.getCalls)
	}

	// Segunda: la sesion sale de Valkey. Que esto funcione es lo que evita un
	// viaje a Postgres en cada peticion autenticada.
	if _, err := h.svc.GetCurrentUser(ctx, &userv1.GetCurrentUserRequest{}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if h.sessions.getCalls != 1 {
		t.Fatalf("consultas de sesion a la BD = %d tras el hit, se esperaba seguir en 1", h.sessions.getCalls)
	}
}

// --- ValidateSession (lo que llaman los servicios vecinos) -------------------

func TestValidateSession_TokenVacio(t *testing.T) {
	h := newHarness()

	_, err := h.svc.ValidateSession(context.Background(), &userv1.ValidateSessionRequest{})

	requireCode(t, err, codes.InvalidArgument)
}

func TestValidateSession_TokenInvalido(t *testing.T) {
	h := newHarness()

	_, err := h.svc.ValidateSession(context.Background(), &userv1.ValidateSessionRequest{
		Token: "token-falsificado",
	})

	// library-service traduce este codigo a un 401 para su cliente; si aqui
	// saliera Internal, un token falso se veria como un fallo del servidor.
	requireCode(t, err, codes.Unauthenticated)
}

func TestValidateSession_ResuelveElUserID(t *testing.T) {
	h := newHarness()

	const crudo = "token-valido"
	h.sessions.valid = &domain.Session{
		UserID: testUserID, TokenHash: token.Hash(crudo), ExpiresAt: time.Now().Add(time.Hour),
	}

	resp, err := h.svc.ValidateSession(context.Background(), &userv1.ValidateSessionRequest{Token: crudo})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resp.GetUserId() != testUserID {
		t.Fatalf("user_id = %q, se esperaba %q", resp.GetUserId(), testUserID)
	}
}

// El token viaja en el cuerpo, no en el header: quien llama es otro servicio
// reenviando el token de su cliente, asi que no debe depender de la metadata.
func TestValidateSession_NoDependeDelHeader(t *testing.T) {
	h := newHarness()

	const crudo = "token-valido"
	h.sessions.valid = &domain.Session{
		UserID: testUserID, TokenHash: token.Hash(crudo), ExpiresAt: time.Now().Add(time.Hour),
	}

	// Contexto sin metadata alguna.
	resp, err := h.svc.ValidateSession(context.Background(), &userv1.ValidateSessionRequest{Token: crudo})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resp.GetUserId() != testUserID {
		t.Fatalf("user_id = %q", resp.GetUserId())
	}
}
