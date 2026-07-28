package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/domain"
	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/repository"
	userv1 "github.com/EstebanTech/lectonautas/backend/microservices/user-service/proto/user/v1"
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

// El perfil publico no lleva email ni is_active: GetUser no pide token y el id
// de un autor viaja en cada libro, asi que lo que devuelve lo puede leer
// cualquiera. Que el tipo PublicUser no tenga esos campos es lo que hace que la
// fuga no se pueda reintroducir por descuido; esto fija el resto del contrato.
func TestGetUser_DevuelveElPerfilPublico(t *testing.T) {
	h := newHarness()
	u := h.conUsuario(t, true)
	bio := "Escribo de noche"
	u.Bio = &bio

	resp, err := h.svc.GetUser(context.Background(), &userv1.GetUserRequest{Id: testUserID})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	got := resp.GetUser()
	if got.GetId() != testUserID || got.GetUsername() != testUsername || got.GetBio() != bio {
		t.Fatalf("al perfil publico le falta lo que si es publico: %+v", got)
	}

	// Y el email no puede aparecer por ninguna via: ni siquiera serializado.
	if strings.Contains(got.String(), testEmail) {
		t.Fatalf("el email se filtro en el perfil publico: %s", got.String())
	}
}

func TestGetAllUsers_NoFiltraEmails(t *testing.T) {
	h := newHarness()
	h.conUsuario(t, true)

	resp, err := h.svc.GetAllUsers(context.Background(), &userv1.GetAllUsersRequest{})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(resp.GetUsers()) != 1 {
		t.Fatalf("usuarios = %d, se esperaba 1", len(resp.GetUsers()))
	}
	if strings.Contains(resp.String(), testEmail) {
		t.Fatalf("el listado publico llevaba emails: %s", resp.String())
	}
}

// La contraparte: el dueno si recibe su email, o no podria ver sus propios
// datos en ningun sitio.
func TestGetCurrentUser_SiLlevaElEmail(t *testing.T) {
	h := newHarness()
	h.conUsuario(t, true)
	ctx := h.conSesion(context.Background(), testUserID)

	resp, err := h.svc.GetCurrentUser(ctx, &userv1.GetCurrentUserRequest{})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resp.GetUser().GetEmail() != testEmail {
		t.Fatalf("email = %q, se esperaba %q", resp.GetUser().GetEmail(), testEmail)
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
			ctx := h.conSesion(context.Background(), testUserID)
			_, err := h.svc.UpdateUser(ctx, tt.req)
			requireCode(t, err, codes.InvalidArgument)
		})
	}
}

// Sin esto, bastaba con conocer un id ajeno —y los ids de autor son publicos,
// van en cada libro— para reescribir el perfil de cualquiera.
func TestUpdateUser_SoloElDueno(t *testing.T) {
	const otroID = "22222222-2222-2222-2222-222222222222"
	nuevo := "secuestrado"

	tests := []struct {
		nombre string
		ctx    func(*harness) context.Context
		quiere codes.Code
	}{
		{"sin token", func(*harness) context.Context {
			return context.Background()
		}, codes.Unauthenticated},
		{"con el token de otro", func(h *harness) context.Context {
			return h.conSesion(context.Background(), otroID)
		}, codes.PermissionDenied},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			h := newHarness()
			h.conUsuario(t, true)

			_, err := h.svc.UpdateUser(tt.ctx(h), &userv1.UpdateUserRequest{
				Id: testUserID, Username: &nuevo,
			})

			requireCode(t, err, tt.quiere)
			if u := h.repo.users[testUserID]; u.Username != testUsername {
				t.Fatalf("el perfil se modifico igual: username = %q", u.Username)
			}
		})
	}
}

func TestUpdateUser_InvalidaElCacheDelUsuario(t *testing.T) {
	h := newHarness()
	h.conUsuario(t, true)
	ctx := h.conSesion(context.Background(), testUserID)

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
	ctx := h.conSesion(context.Background(), testUserID)

	_, err := h.svc.UpdateUser(ctx, &userv1.UpdateUserRequest{
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
	ctx := h.conSesion(context.Background(), testUserID)
	_, err := h.svc.DeleteUser(ctx, &userv1.DeleteUserRequest{Id: testUserID})
	requireCode(t, err, codes.NotFound)
}

func TestDeleteUser_InvalidaElCache(t *testing.T) {
	h := newHarness()
	h.conUsuario(t, true)
	ctx := h.conSesion(context.Background(), testUserID)

	if _, err := h.svc.DeleteUser(ctx, &userv1.DeleteUserRequest{Id: testUserID}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if len(h.userCache.invalidatedUsers) != 1 {
		t.Fatalf("invalidaciones = %d, se esperaba 1", len(h.userCache.invalidatedUsers))
	}
}

// Borrar la cuenta es lo mas destructivo de la API y con la cascada se lleva
// toda la obra del autor: no puede depender de conocer un id publico.
func TestDeleteUser_SoloElDueno(t *testing.T) {
	const otroID = "22222222-2222-2222-2222-222222222222"

	tests := []struct {
		nombre string
		ctx    func(*harness) context.Context
		quiere codes.Code
	}{
		{"sin token", func(*harness) context.Context {
			return context.Background()
		}, codes.Unauthenticated},
		{"con el token de otro", func(h *harness) context.Context {
			return h.conSesion(context.Background(), otroID)
		}, codes.PermissionDenied},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			h := newHarness()
			h.conUsuario(t, true)

			_, err := h.svc.DeleteUser(tt.ctx(h), &userv1.DeleteUserRequest{Id: testUserID})

			requireCode(t, err, tt.quiere)
			if _, sigue := h.repo.users[testUserID]; !sigue {
				t.Fatal("la cuenta se borro igual")
			}
			if len(h.content.deleted) != 0 {
				t.Fatal("se pidio borrar el contenido de una cuenta ajena")
			}
		})
	}
}

func TestDeleteUser_ArrastraElContenidoDelAutor(t *testing.T) {
	h := newHarness()
	h.conUsuario(t, true)
	ctx := h.conSesion(context.Background(), testUserID)

	if _, err := h.svc.DeleteUser(ctx, &userv1.DeleteUserRequest{Id: testUserID}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	// Los libros y sagas viven en otra base, asi que no hay CASCADE que los
	// alcance: hay que habersele pedido a library-service.
	if len(h.content.deleted) != 1 || h.content.deleted[0] != testUserID {
		t.Fatalf("contenido borrado = %v, se esperaba [%s]", h.content.deleted, testUserID)
	}

	// Y lo mismo con lo que dejo como lector, que vive en una tercera base.
	if len(h.interactions.deleted) != 1 || h.interactions.deleted[0] != testUserID {
		t.Fatalf("interacciones borradas = %v, se esperaba [%s]", h.interactions.deleted, testUserID)
	}
}

// Mismo criterio que con library-service: los vecinos van antes de borrar la
// fila del usuario, y si uno falla la cuenta sigue en pie. Es la unica forma de
// que un reintento pueda arreglarlo — sin el id ya no habria con que pedir la
// limpieza.
func TestDeleteUser_SiFallanLasInteraccionesNoBorraLaCuenta(t *testing.T) {
	h := newHarness()
	h.conUsuario(t, true)
	h.interactions.err = errors.New("interaction-service caido")
	ctx := h.conSesion(context.Background(), testUserID)

	_, err := h.svc.DeleteUser(ctx, &userv1.DeleteUserRequest{Id: testUserID})

	requireCode(t, err, codes.Internal)
	if _, sigue := h.repo.users[testUserID]; !sigue {
		t.Fatal("se borro la cuenta pese a que sus interacciones no se pudieron borrar")
	}
}

// El contenido va primero justamente para esto: si library-service no responde,
// la cuenta sigue en pie y el cliente puede reintentar. Al reves quedaria obra
// sin dueno y sin forma de llegar a ella.
func TestDeleteUser_SiFallaElContenidoNoBorraLaCuenta(t *testing.T) {
	h := newHarness()
	h.conUsuario(t, true)
	h.content.err = errors.New("library-service caido")
	ctx := h.conSesion(context.Background(), testUserID)

	_, err := h.svc.DeleteUser(ctx, &userv1.DeleteUserRequest{Id: testUserID})

	requireCode(t, err, codes.Internal)
	if _, sigue := h.repo.users[testUserID]; !sigue {
		t.Fatal("se borro la cuenta pese a que su contenido no se pudo borrar")
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

// Al darse de baja, los tokens de la cuenta tienen que morir con ella. No basta
// con que el CASCADE se lleve las filas de session: resolveSession consulta
// Valkey ANTES que la BD, asi que una entrada que sobreviva sigue resolviendo el
// token durante toda la ventana de sessionCacheTTL. Y no solo aqui — los vecinos
// validan sus tokens contra este servicio, asi que con uno de esos se podian
// seguir creando libros a nombre de una cuenta que ya no existe.
func TestDeleteUser_TiraLasSesionesCacheadas(t *testing.T) {
	h := newHarness()
	h.conUsuario(t, true)
	ctx := h.conSesion(context.Background(), testUserID)

	// La sesion existe tambien en la "BD", que es de donde salen los hashes.
	hash := soloHashDeLaSesion(t, h)
	h.sessions.valid = &domain.Session{UserID: testUserID, TokenHash: hash}

	if _, err := h.svc.DeleteUser(ctx, &userv1.DeleteUserRequest{Id: testUserID}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if _, sigue := h.sessionCache.entries[hash]; sigue {
		t.Fatal("la sesion sigue cacheada: el token de una cuenta borrada todavia resolveria")
	}
}

// Si no se pueden leer los hashes, la baja tiene que completarse igual: es peor
// una cuenta que no se puede borrar que unas sesiones que caducan solas.
func TestDeleteUser_SeCompletaAunqueNoSePuedanLeerLasSesiones(t *testing.T) {
	h := newHarness()
	h.conUsuario(t, true)
	ctx := h.conSesion(context.Background(), testUserID)
	h.sessions.hashesErr = errors.New("postgres caido")

	if _, err := h.svc.DeleteUser(ctx, &userv1.DeleteUserRequest{Id: testUserID}); err != nil {
		t.Fatalf("la baja no deberia fallar por el listado de sesiones: %v", err)
	}
	if _, sigue := h.repo.users[testUserID]; sigue {
		t.Fatal("la cuenta no se borro")
	}
}

// soloHashDeLaSesion devuelve el unico hash que conSesion dejo en el cache.
func soloHashDeLaSesion(t *testing.T, h *harness) string {
	t.Helper()
	if len(h.sessionCache.entries) != 1 {
		t.Fatalf("se esperaba 1 sesion cacheada, hay %d", len(h.sessionCache.entries))
	}
	for hash := range h.sessionCache.entries {
		return hash
	}
	return ""
}
