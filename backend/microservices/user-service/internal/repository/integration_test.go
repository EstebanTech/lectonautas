package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/estebandeveloper20/lectonautas/backend/microservices/user-service/internal/domain"
)

// Pruebas contra una base real. Cubren lo que los dobles en memoria no pueden:
// los UNIQUE del esquema traducidos a errores de dominio, el CASCADE de las
// sesiones, y sobre todo las condiciones de vigencia de la sesion, que viven
// enteras en el WHERE de la consulta y no en Go.
//
// Se saltan solas si no hay base configurada:
//
//	USER_TEST_DATABASE_URL="postgres://user:password@localhost:5433/user_service?sslmode=disable" go test ./internal/repository/

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("USER_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("USER_TEST_DATABASE_URL no esta definida; se omiten las pruebas de integracion")
	}

	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("no se pudo abrir el pool: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("no se pudo conectar a la base: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

// newUser crea un usuario con datos unicos y programa su borrado. Las sesiones
// se van por CASCADE.
func newUser(t *testing.T, repo *PostgresUserRepository, pool *pgxpool.Pool) *domain.User {
	t.Helper()

	sufijo := fmt.Sprintf("%d", time.Now().UnixNano())
	u, err := repo.Create(context.Background(), &domain.User{
		Email:    "test-" + sufijo + "@lectonautas.dev",
		Username: "test-" + sufijo[len(sufijo)-12:],
		Password: "hash-de-prueba",
	})
	if err != nil {
		t.Fatalf("no se pudo crear el usuario: %v", err)
	}

	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID)
	})

	return u
}

func TestCreateUser_EmailYUsernameUnicos(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	repo := NewPostgresUserRepository(pool)

	existente := newUser(t, repo, pool)

	t.Run("email repetido", func(t *testing.T) {
		_, err := repo.Create(ctx, &domain.User{
			Email: existente.Email, Username: "otro-username-distinto", Password: "x",
		})
		// El UNIQUE del esquema tiene que llegar como ErrEmailTaken para que el
		// servicio responda AlreadyExists y no un 500.
		if !errors.Is(err, ErrEmailTaken) {
			t.Fatalf("error = %v, se esperaba ErrEmailTaken", err)
		}
	})

	t.Run("username repetido", func(t *testing.T) {
		_, err := repo.Create(ctx, &domain.User{
			Email: "otro-correo-distinto@lectonautas.dev", Username: existente.Username, Password: "x",
		})
		if !errors.Is(err, ErrUsernameTaken) {
			t.Fatalf("error = %v, se esperaba ErrUsernameTaken", err)
		}
	})
}

func TestGetByID_NuncaDevuelveElHashDeLaPassword(t *testing.T) {
	pool := testPool(t)
	repo := NewPostgresUserRepository(pool)

	creado := newUser(t, repo, pool)

	leido, err := repo.GetByID(context.Background(), creado.ID)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	// Las consultas de lectura no piden la columna password a proposito: asi
	// el hash no puede acabar en un log, en el cache ni en una respuesta.
	if leido.Password != "" {
		t.Fatalf("GetByID devolvio la password: %q", leido.Password)
	}
}

func TestGetByEmailForAuth_SiTraeElHash(t *testing.T) {
	pool := testPool(t)
	repo := NewPostgresUserRepository(pool)

	creado := newUser(t, repo, pool)

	// Es la unica consulta que debe traerlo: sin el no se puede comparar la
	// password en el login.
	leido, err := repo.GetByEmailForAuth(context.Background(), creado.Email)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if leido.Password != "hash-de-prueba" {
		t.Fatalf("password = %q, se esperaba el hash guardado", leido.Password)
	}
}

func TestGetByID_UUIDMalformadoEsNotFound(t *testing.T) {
	pool := testPool(t)
	repo := NewPostgresUserRepository(pool)

	_, err := repo.GetByID(context.Background(), "no-soy-un-uuid")

	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("error = %v, se esperaba ErrUserNotFound", err)
	}
}

// --- Sesiones ---------------------------------------------------------------

func TestSession_SoloResuelveLasVigentes(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewPostgresUserRepository(pool)
	sessions := NewPostgresSessionRepository(pool)

	u := newUser(t, users, pool)

	tests := []struct {
		nombre    string
		expiresAt time.Time
		revocar   bool
		wantValid bool
	}{
		{"vigente", time.Now().Add(time.Hour), false, true},
		{"expirada", time.Now().Add(-time.Hour), false, false},
		{"revocada", time.Now().Add(time.Hour), true, false},
	}

	for i, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			hash := fmt.Sprintf("%064d", i+int(time.Now().UnixNano()%1000)*10)
			s := &domain.Session{UserID: u.ID, TokenHash: hash, ExpiresAt: tt.expiresAt}
			if err := sessions.Create(ctx, s); err != nil {
				t.Fatalf("no se pudo crear la sesion: %v", err)
			}
			if tt.revocar {
				if err := sessions.Revoke(ctx, hash); err != nil {
					t.Fatalf("no se pudo revocar: %v", err)
				}
			}

			got, err := sessions.GetValidByTokenHash(ctx, hash)

			if tt.wantValid {
				if err != nil {
					t.Fatalf("la sesion deberia ser valida: %v", err)
				}
				if got.UserID != u.ID {
					t.Fatalf("user_id = %q, se esperaba %q", got.UserID, u.ID)
				}
				// GetValidByTokenHash refresca last_used_at de paso.
				if got.LastUsedAt == nil {
					t.Fatal("no se refresco last_used_at")
				}
				return
			}
			if !errors.Is(err, ErrSessionNotFound) {
				t.Fatalf("error = %v, se esperaba ErrSessionNotFound", err)
			}
		})
	}
}

func TestSession_RevokeDosVecesNoEsIdempotente(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewPostgresUserRepository(pool)
	sessions := NewPostgresSessionRepository(pool)

	u := newUser(t, users, pool)
	hash := fmt.Sprintf("%064d", time.Now().UnixNano())
	if err := sessions.Create(ctx, &domain.Session{
		UserID: u.ID, TokenHash: hash, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("no se pudo crear la sesion: %v", err)
	}

	if err := sessions.Revoke(ctx, hash); err != nil {
		t.Fatalf("el primer logout deberia funcionar: %v", err)
	}

	// El segundo no toca ninguna fila (el WHERE exige revoked_at IS NULL). Es
	// lo que hace que un logout repetido responda 401 y no un falso exito.
	err := sessions.Revoke(ctx, hash)
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("error = %v, se esperaba ErrSessionNotFound", err)
	}
}

func TestDeleteUser_ArrastraSusSesiones(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewPostgresUserRepository(pool)
	sessions := NewPostgresSessionRepository(pool)

	u := newUser(t, users, pool)
	hash := fmt.Sprintf("%064d", time.Now().UnixNano())
	if err := sessions.Create(ctx, &domain.Session{
		UserID: u.ID, TokenHash: hash, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("no se pudo crear la sesion: %v", err)
	}

	if err := users.Delete(ctx, u.ID); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	// Sin el CASCADE quedarian sesiones huerfanas apuntando a un usuario que
	// ya no existe.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM session WHERE user_id = $1`, u.ID).Scan(&n); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if n != 0 {
		t.Fatalf("quedaron %d sesiones tras borrar el usuario", n)
	}
}

func TestUpdateUser_ParcialNoPisaLoQueNoViene(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	repo := NewPostgresUserRepository(pool)

	u := newUser(t, repo, pool)
	nuevoUsername := "renombrado-" + fmt.Sprintf("%d", time.Now().UnixNano()%100000000)

	actualizado, err := repo.Update(ctx, &domain.UserUpdate{ID: u.ID, Username: &nuevoUsername})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if actualizado.Username != nuevoUsername {
		t.Fatalf("username = %q, se esperaba %q", actualizado.Username, nuevoUsername)
	}
	// El email no venia en el update: tiene que seguir igual.
	if actualizado.Email != u.Email {
		t.Fatalf("el update piso el email: %q", actualizado.Email)
	}
	if !actualizado.UpdatedAt.After(u.UpdatedAt) {
		t.Fatal("no se refresco updated_at")
	}
}
