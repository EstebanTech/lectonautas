package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/domain"
)

var ErrSessionNotFound = errors.New("session not found")

type SessionRepository interface {
	Create(ctx context.Context, s *domain.Session) error
	GetValidByTokenHash(ctx context.Context, tokenHash string) (*domain.Session, error)
	Revoke(ctx context.Context, tokenHash string) error
	// TokenHashesByUser devuelve los hashes de las sesiones vigentes del
	// usuario. Lo usa la baja de cuenta para poder tirar tambien las entradas
	// que esas sesiones tengan en Valkey: borrar la fila no alcanza, porque el
	// cache se consulta antes que la BD.
	TokenHashesByUser(ctx context.Context, userID string) ([]string, error)
}

type PostgresSessionRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresSessionRepository(pool *pgxpool.Pool) *PostgresSessionRepository {
	return &PostgresSessionRepository{pool: pool}
}

func (r *PostgresSessionRepository) Create(ctx context.Context, s *domain.Session) error {
	const query = `
		INSERT INTO session (user_id, token, user_agent, expires_at)
		VALUES ($1, $2, $3, $4)
		RETURNING id::text, created_at`

	return r.pool.QueryRow(ctx, query, s.UserID, s.TokenHash, s.UserAgent, s.ExpiresAt).
		Scan(&s.ID, &s.CreatedAt)
}

// GetValidByTokenHash devuelve la sesion solo si sigue vigente: ni revocada ni
// expirada. De paso refresca last_used_at para saber cuando se uso por ultima
// vez.
func (r *PostgresSessionRepository) GetValidByTokenHash(ctx context.Context, tokenHash string) (*domain.Session, error) {
	const query = `
		UPDATE session
		SET last_used_at = now()
		WHERE token = $1 AND revoked_at IS NULL AND expires_at > now()
		RETURNING id::text, user_id::text, token, user_agent, last_used_at, expires_at, revoked_at, created_at`

	s := &domain.Session{}
	err := r.pool.QueryRow(ctx, query, tokenHash).Scan(
		&s.ID, &s.UserID, &s.TokenHash, &s.UserAgent, &s.LastUsedAt, &s.ExpiresAt, &s.RevokedAt, &s.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// TokenHashesByUser lista las sesiones que todavia podrian resolverse: las no
// revocadas y sin vencer. Las demas ya no abren nada, ni desde el cache, porque
// la entrada de Valkey nunca vive mas que el expires_at de su sesion.
func (r *PostgresSessionRepository) TokenHashesByUser(ctx context.Context, userID string) ([]string, error) {
	const query = `
		SELECT token FROM session
		WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()`

	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	hashes := make([]string, 0)
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, err
		}
		hashes = append(hashes, hash)
	}
	return hashes, rows.Err()
}

// Revoke marca la sesion como revocada (logout). No borra la fila para dejar
// rastro de la sesion cerrada.
func (r *PostgresSessionRepository) Revoke(ctx context.Context, tokenHash string) error {
	const query = `UPDATE session SET revoked_at = now() WHERE token = $1 AND revoked_at IS NULL`

	tag, err := r.pool.Exec(ctx, query, tokenHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionNotFound
	}
	return nil
}
