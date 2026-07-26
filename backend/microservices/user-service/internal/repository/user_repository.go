package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/domain"
)

var (
	ErrUserNotFound  = errors.New("user not found")
	ErrEmailTaken    = errors.New("email already registered")
	ErrUsernameTaken = errors.New("username already taken")
)

// Columnas devueltas por todas las consultas: el password hasheado nunca se
// lee de vuelta, solo se escribe.
const userColumns = `id::text, email, username, display_name, avatar_url, bio, is_active, created_at, updated_at`

type UserRepository interface {
	Create(ctx context.Context, u *domain.User) (*domain.User, error)
	GetByID(ctx context.Context, id string) (*domain.User, error)
	GetByEmailForAuth(ctx context.Context, email string) (*domain.User, error)
	Update(ctx context.Context, upd *domain.UserUpdate) (*domain.User, error)
	Delete(ctx context.Context, id string) error
	GetAll(ctx context.Context) ([]*domain.User, error)
}

type PostgresUserRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresUserRepository(pool *pgxpool.Pool) *PostgresUserRepository {
	return &PostgresUserRepository{pool: pool}
}

func (r *PostgresUserRepository) Create(ctx context.Context, u *domain.User) (*domain.User, error) {
	const query = `
		INSERT INTO users (email, username, password, display_name, avatar_url, bio)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING ` + userColumns

	row := r.pool.QueryRow(ctx, query, u.Email, u.Username, u.Password, u.DisplayName, u.AvatarURL, u.Bio)
	out, err := scanUser(row)
	if err != nil {
		return nil, translateErr(err)
	}
	return out, nil
}

func (r *PostgresUserRepository) GetByID(ctx context.Context, id string) (*domain.User, error) {
	const query = `SELECT ` + userColumns + ` FROM users WHERE id = $1`

	out, err := scanUser(r.pool.QueryRow(ctx, query, id))
	if err != nil {
		return nil, translateErr(err)
	}
	return out, nil
}

// GetByEmailForAuth trae el usuario con su hash de password, que las demas
// consultas nunca leen. Es solo para el login: comparar la password ingresada.
func (r *PostgresUserRepository) GetByEmailForAuth(ctx context.Context, email string) (*domain.User, error) {
	const query = `
		SELECT id::text, email, username, password, display_name, avatar_url, bio, is_active, created_at, updated_at
		FROM users WHERE email = $1`

	u := &domain.User{}
	err := r.pool.QueryRow(ctx, query, email).Scan(
		&u.ID, &u.Email, &u.Username, &u.Password, &u.DisplayName, &u.AvatarURL, &u.Bio, &u.IsActive, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		return nil, translateErr(err)
	}
	return u, nil
}

func (r *PostgresUserRepository) Update(ctx context.Context, upd *domain.UserUpdate) (*domain.User, error) {
	sets := []string{"updated_at = now()"}
	args := []any{upd.ID}

	set := func(column string, value any) {
		args = append(args, value)
		sets = append(sets, fmt.Sprintf("%s = $%d", column, len(args)))
	}

	if upd.Username != nil {
		set("username", *upd.Username)
	}
	if upd.DisplayName != nil {
		set("display_name", nullIfEmpty(*upd.DisplayName))
	}
	if upd.AvatarURL != nil {
		set("avatar_url", nullIfEmpty(*upd.AvatarURL))
	}
	if upd.Bio != nil {
		set("bio", nullIfEmpty(*upd.Bio))
	}
	if upd.IsActive != nil {
		set("is_active", *upd.IsActive)
	}

	query := `UPDATE users SET ` + strings.Join(sets, ", ") + ` WHERE id = $1 RETURNING ` + userColumns

	out, err := scanUser(r.pool.QueryRow(ctx, query, args...))
	if err != nil {
		return nil, translateErr(err)
	}
	return out, nil
}

func (r *PostgresUserRepository) Delete(ctx context.Context, id string) error {
	const query = `DELETE FROM users WHERE id = $1`

	tag, err := r.pool.Exec(ctx, query, id)
	if err != nil {
		return translateErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}

// GetAll devuelve todos los usuarios, sin paginacion ni filtros: tambien vienen
// los inactivos. Ya no hace falta un count aparte porque el total es cuantas
// filas se trajeron.
func (r *PostgresUserRepository) GetAll(ctx context.Context) ([]*domain.User, error) {
	const query = `
		SELECT ` + userColumns + `
		FROM users
		ORDER BY created_at DESC`

	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := make([]*domain.User, 0)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return users, nil
}

// scanner cubre tanto pgx.Row (QueryRow) como pgx.Rows (Query), que exponen
// la misma firma de Scan.
type scanner interface {
	Scan(dest ...any) error
}

func scanUser(s scanner) (*domain.User, error) {
	u := &domain.User{}
	err := s.Scan(
		&u.ID,
		&u.Email,
		&u.Username,
		&u.DisplayName,
		&u.AvatarURL,
		&u.Bio,
		&u.IsActive,
		&u.CreatedAt,
		&u.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// translateErr convierte errores de Postgres en errores de dominio para que el
// servicio pueda mapearlos a codigos gRPC sin conocer detalles del driver.
func translateErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUserNotFound
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		// invalid_text_representation: el id recibido no es un UUID valido,
		// asi que no puede corresponder a ningun usuario.
		case pgErr.Code == "22P02":
			return ErrUserNotFound
		case pgErr.Code == "23505" && pgErr.ConstraintName == "users_email_key":
			return ErrEmailTaken
		case pgErr.Code == "23505" && pgErr.ConstraintName == "users_username_key":
			return ErrUsernameTaken
		}
	}

	return err
}
