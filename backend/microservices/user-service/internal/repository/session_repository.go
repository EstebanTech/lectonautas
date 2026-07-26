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
