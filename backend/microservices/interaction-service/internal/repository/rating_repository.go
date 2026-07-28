package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/domain"
)

type RatingRepository interface {
	// Rate deja la calificacion del lector en score y devuelve el resumen ya
	// actualizado. Si ya habia votado, la reemplaza.
	Rate(ctx context.Context, bookID, userID string, score int32) (*domain.RatingSummary, error)
	// Delete quita el voto del lector. Devuelve ErrRatingNotFound si no habia.
	Delete(ctx context.Context, bookID, userID string) error
	// Summary es la lectura. viewerID vacio devuelve MyScore en 0.
	Summary(ctx context.Context, bookID, viewerID string) (*domain.RatingSummary, error)
}

type PostgresRatingRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresRatingRepository(pool *pgxpool.Pool) *PostgresRatingRepository {
	return &PostgresRatingRepository{pool: pool}
}

// Rate es un upsert sobre la PK (book_id, user_id): un lector tiene una sola
// calificacion por libro, asi que volver a votar es cambiar la suya, no sumar
// otro voto. Hacerlo en un solo statement evita la carrera entre comprobar si
// ya voto e insertar.
func (r *PostgresRatingRepository) Rate(ctx context.Context, bookID, userID string, score int32) (*domain.RatingSummary, error) {
	const query = `
		INSERT INTO interaction.ratings (book_id, user_id, score)
		VALUES ($1, $2, $3)
		ON CONFLICT (book_id, user_id)
		DO UPDATE SET score = EXCLUDED.score, updated_at = now()`

	if _, err := r.pool.Exec(ctx, query, bookID, userID, score); err != nil {
		return nil, translateErr(err, ErrBookNotFound)
	}
	return r.Summary(ctx, bookID, userID)
}

func (r *PostgresRatingRepository) Delete(ctx context.Context, bookID, userID string) error {
	const query = `DELETE FROM interaction.ratings WHERE book_id = $1 AND user_id = $2`

	tag, err := r.pool.Exec(ctx, query, bookID, userID)
	if err != nil {
		return translateErr(err, ErrRatingNotFound)
	}
	if tag.RowsAffected() == 0 {
		return ErrRatingNotFound
	}
	return nil
}

// Summary saca promedio, cantidad y voto propio de una sola pasada.
//
// El COALESCE del promedio es lo que hace que un libro sin votos devuelva 0 y
// no null; el del voto propio, que un lector que no voto (o un anonimo, que
// llega con cadena vacia y por eso el NULLIF) devuelva 0.
func (r *PostgresRatingRepository) Summary(ctx context.Context, bookID, viewerID string) (*domain.RatingSummary, error) {
	const query = `
		SELECT coalesce(avg(score), 0)::float8,
		       count(*)::int,
		       coalesce(max(score) FILTER (WHERE user_id = NULLIF($2, '')::uuid), 0)::int
		FROM interaction.ratings
		WHERE book_id = $1`

	s := &domain.RatingSummary{BookID: bookID}
	if err := r.pool.QueryRow(ctx, query, bookID, viewerID).Scan(&s.Average, &s.Count, &s.MyScore); err != nil {
		return nil, translateErr(err, ErrBookNotFound)
	}
	return s, nil
}
