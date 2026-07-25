package service

import (
	"context"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/estebandeveloper20/lectonautas/backend/microservices/user-service/internal/repository"
	userv1 "github.com/estebandeveloper20/lectonautas/backend/microservices/user-service/proto/user/v1"
)

func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("se esperaba error con codigo %v, no hubo error", want)
	}
	if got := status.Code(err); got != want {
		t.Fatalf("codigo = %v, se esperaba %v (error: %v)", got, want, err)
	}
}

func hashDePrueba(t interface{ Fatalf(string, ...any) }, password string) string {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("no se pudo hashear la password de prueba: %v", err)
	}
	return string(hash)
}

// --- CreateUser -------------------------------------------------------------

func TestCreateUser_ValidaLaEntrada(t *testing.T) {
	tests := []struct {
		nombre string
		req    *userv1.CreateUserRequest
	}{
		{"email vacio", &userv1.CreateUserRequest{Username: "autor", Password: testPassword}},
		{"email sin arroba", &userv1.CreateUserRequest{Email: "noesuncorreo", Username: "autor", Password: testPassword}},
		{"username vacio", &userv1.CreateUserRequest{Email: testEmail, Password: testPassword}},
		{"username muy corto", &userv1.CreateUserRequest{Email: testEmail, Username: "ab", Password: testPassword}},
		{"username con espacios", &userv1.CreateUserRequest{Email: testEmail, Username: "con espacio", Password: testPassword}},
		{"username con acentos", &userv1.CreateUserRequest{Email: testEmail, Username: "señor", Password: testPassword}},
		{"password vacia", &userv1.CreateUserRequest{Email: testEmail, Username: "autor"}},
		{"password muy corta", &userv1.CreateUserRequest{Email: testEmail, Username: "autor", Password: "corta"}},
		{"password mas larga que el limite de bcrypt",
			&userv1.CreateUserRequest{Email: testEmail, Username: "autor", Password: strings.Repeat("x", 73)}},
		{"bio demasiado larga",
			&userv1.CreateUserRequest{Email: testEmail, Username: "autor", Password: testPassword, Bio: strings.Repeat("x", 1001)}},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			h := newHarness()
			_, err := h.svc.CreateUser(context.Background(), tt.req)
			requireCode(t, err, codes.InvalidArgument)
		})
	}
}

func TestCreateUser_NormalizaEmailYUsername(t *testing.T) {
	h := newHarness()

	_, err := h.svc.CreateUser(context.Background(), &userv1.CreateUserRequest{
		Email:    "  AUTOR@Lectonautas.DEV  ",
		Username: "  AUTOR  ",
		Password: testPassword,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	// Si no se normalizaran, el mismo correo con otra capitalizacion crearia
	// una cuenta distinta y el login fallaria segun como se escribiera.
	if h.repo.created.Email != testEmail {
		t.Fatalf("email guardado = %q, se esperaba %q", h.repo.created.Email, testEmail)
	}
	if h.repo.created.Username != testUsername {
		t.Fatalf("username guardado = %q, se esperaba %q", h.repo.created.Username, testUsername)
	}
}

func TestCreateUser_NuncaGuardaLaPasswordEnClaro(t *testing.T) {
	h := newHarness()

	_, err := h.svc.CreateUser(context.Background(), &userv1.CreateUserRequest{
		Email: testEmail, Username: testUsername, Password: testPassword,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	guardada := h.repo.created.Password
	if guardada == testPassword {
		t.Fatal("la password llego al repositorio en claro")
	}
	// Y lo guardado tiene que ser un hash valido de esa password.
	if err := bcrypt.CompareHashAndPassword([]byte(guardada), []byte(testPassword)); err != nil {
		t.Fatalf("lo guardado no es un hash bcrypt de la password: %v", err)
	}
}

func TestCreateUser_TraduceLosConflictos(t *testing.T) {
	tests := []struct {
		nombre  string
		repoErr error
	}{
		{"email ya registrado", repository.ErrEmailTaken},
		{"username ya tomado", repository.ErrUsernameTaken},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			h := newHarness()
			h.repo.createErr = tt.repoErr

			_, err := h.svc.CreateUser(context.Background(), &userv1.CreateUserRequest{
				Email: testEmail, Username: testUsername, Password: testPassword,
			})

			// AlreadyExists, no Internal: es un error del cliente.
			requireCode(t, err, codes.AlreadyExists)
		})
	}
}

func TestCreateUser_InvalidaElListadoCompleto(t *testing.T) {
	h := newHarness()

	_, err := h.svc.CreateUser(context.Background(), &userv1.CreateUserRequest{
		Email: testEmail, Username: testUsername, Password: testPassword,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	// Sin esto, GetAllUsers seguiria sirviendo una lista sin el usuario nuevo
	// hasta que venciera el TTL.
	if h.userCache.invalidatedAll != 1 {
		t.Fatalf("invalidaciones del listado = %d, se esperaba 1", h.userCache.invalidatedAll)
	}
}

// --- GetUser ----------------------------------------------------------------

func TestGetUser_IdVacio(t *testing.T) {
	h := newHarness()
	_, err := h.svc.GetUser(context.Background(), &userv1.GetUserRequest{})
	requireCode(t, err, codes.InvalidArgument)
}

func TestGetUser_NoExiste(t *testing.T) {
	h := newHarness()
	_, err := h.svc.GetUser(context.Background(), &userv1.GetUserRequest{Id: testUserID})
	requireCode(t, err, codes.NotFound)
}

func TestGetUser_CacheAside(t *testing.T) {
	h := newHarness()
	h.conUsuario(t, true)
	ctx := context.Background()

	// Primera lectura: miss, va a la BD y repuebla.
	if _, err := h.svc.GetUser(ctx, &userv1.GetUserRequest{Id: testUserID}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if h.repo.getByIDCalls != 1 {
		t.Fatalf("consultas a la BD = %d, se esperaba 1", h.repo.getByIDCalls)
	}

	// Segunda: hit, no debe volver a la BD. Si vuelve, el cache no sirve de nada.
	if _, err := h.svc.GetUser(ctx, &userv1.GetUserRequest{Id: testUserID}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if h.repo.getByIDCalls != 1 {
		t.Fatalf("consultas a la BD = %d tras el hit, se esperaba seguir en 1", h.repo.getByIDCalls)
	}
}

// --- UpdateUser -------------------------------------------------------------

func TestUpdateUser_ValidaLaEntrada(t *testing.T) {
	conEspacio := "con espacio"
	muyCorto := "ab"
	bioLarga := strings.Repeat("x", 1001)

	tests := []struct {
		nombre string
		req    *userv1.UpdateUserRequest
	}{
		{"id vacio", &userv1.UpdateUserRequest{}},
		{"sin campos que actualizar", &userv1.UpdateUserRequest{Id: testUserID}},
		{"username con espacios", &userv1.UpdateUserRequest{Id: testUserID, Username: &conEspacio}},
		{"username muy corto", &userv1.UpdateUserRequest{Id: testUserID, Username: &muyCorto}},
		{"bio demasiado larga", &userv1.UpdateUserRequest{Id: testUserID, Bio: &bioLarga}},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			h := newHarness()
			h.conUsuario(t, true)
			_, err := h.svc.UpdateUser(context.Background(), tt.req)
			requireCode(t, err, codes.InvalidArgument)
		})
	}
}

func TestUpdateUser_InvalidaElCacheDelUsuario(t *testing.T) {
	h := newHarness()
	h.conUsuario(t, true)
	ctx := context.Background()

	// Deja el usuario cacheado.
	if _, err := h.svc.GetUser(ctx, &userv1.GetUserRequest{Id: testUserID}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	nuevo := "renombrado"
	if _, err := h.svc.UpdateUser(ctx, &userv1.UpdateUserRequest{Id: testUserID, Username: &nuevo}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(h.userCache.invalidatedUsers) != 1 || h.userCache.invalidatedUsers[0] != testUserID {
		t.Fatalf("invalidaciones = %v, se esperaba [%s]", h.userCache.invalidatedUsers, testUserID)
	}

	// Y la siguiente lectura tiene que volver a la BD, no servir lo viejo.
	antes := h.repo.getByIDCalls
	if _, err := h.svc.GetUser(ctx, &userv1.GetUserRequest{Id: testUserID}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if h.repo.getByIDCalls != antes+1 {
		t.Fatal("tras el update, la lectura siguio saliendo del cache")
	}
}

func TestUpdateUser_NoExiste(t *testing.T) {
	h := newHarness()
	nuevo := "renombrado"

	_, err := h.svc.UpdateUser(context.Background(), &userv1.UpdateUserRequest{
		Id: testUserID, Username: &nuevo,
	})

	requireCode(t, err, codes.NotFound)
}

// --- DeleteUser -------------------------------------------------------------

func TestDeleteUser_IdVacio(t *testing.T) {
	h := newHarness()
	_, err := h.svc.DeleteUser(context.Background(), &userv1.DeleteUserRequest{})
	requireCode(t, err, codes.InvalidArgument)
}

func TestDeleteUser_NoExiste(t *testing.T) {
	h := newHarness()
	_, err := h.svc.DeleteUser(context.Background(), &userv1.DeleteUserRequest{Id: testUserID})
	requireCode(t, err, codes.NotFound)
}

func TestDeleteUser_InvalidaElCache(t *testing.T) {
	h := newHarness()
	h.conUsuario(t, true)

	if _, err := h.svc.DeleteUser(context.Background(), &userv1.DeleteUserRequest{Id: testUserID}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(h.userCache.invalidatedUsers) != 1 {
		t.Fatalf("invalidaciones = %d, se esperaba 1", len(h.userCache.invalidatedUsers))
	}
}

// --- GetAllUsers ------------------------------------------------------------

func TestGetAllUsers_CacheAside(t *testing.T) {
	h := newHarness()
	h.conUsuario(t, true)
	ctx := context.Background()

	resp, err := h.svc.GetAllUsers(ctx, &userv1.GetAllUsersRequest{})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resp.GetTotal() != 1 {
		t.Fatalf("total = %d, se esperaba 1", resp.GetTotal())
	}
	if h.repo.getAllCalls != 1 {
		t.Fatalf("consultas a la BD = %d, se esperaba 1", h.repo.getAllCalls)
	}

	if _, err := h.svc.GetAllUsers(ctx, &userv1.GetAllUsersRequest{}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if h.repo.getAllCalls != 1 {
		t.Fatalf("consultas a la BD = %d tras el hit, se esperaba seguir en 1", h.repo.getAllCalls)
	}
}
