package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/domain"
)

type LikeRepository interface {
	// Like deja el me gusta puesto y devuelve el resumen ya actualizado. Es
	// idempotente: si ya estaba, no falla ni cuenta dos veces.
	Like(ctx context.Context, bookID, userID string) (*domain.LikeSummary, error)
	// Unlike lo quita. Tambien es idempotente: quitar lo que no estaba deja el
	// mismo estado y no es un error.
	Unlike(ctx context.Context, bookID, userID string) (*domain.LikeSummary, error)
	// Summary es la lectura. viewerID vacio (sin token) devuelve LikedByMe en
	// false sin consultarlo.
	Summary(ctx context.Context, bookID, viewerID string) (*domain.LikeSummary, error)
}

type PostgresLikeRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresLikeRepository(pool *pgxpool.Pool) *PostgresLikeRepository {
	return &PostgresLikeRepository{pool: pool}
}

// Like usa ON CONFLICT DO NOTHING en vez de comprobar antes si existe: hacerlo
// en dos pasos dejaria una ventana en la que dos peticiones simultaneas del
// mismo lector pasarian las dos y una chocaria con la PK.
func (r *PostgresLikeRepository) Like(ctx context.Context, bookID, userID string) (*domain.LikeSummary, error) {
	const query = `
		INSERT INTO interaction.likes (book_id, user_id)
		VALUES ($1, $2)
		ON CONFLICT (book_id, user_id) DO NOTHING`

	if _, err := r.pool.Exec(ctx, query, bookID, userID); err != nil {
		return nil, translateErr(err, ErrBookNotFound)
	}
	return r.Summary(ctx, bookID, userID)
}

func (r *PostgresLikeRepository) Unlike(ctx context.Context, bookID, userID string) (*domain.LikeSummary, error) {
	const query = `DELETE FROM interaction.likes WHERE book_id = $1 AND user_id = $2`

	if _, err := r.pool.Exec(ctx, query, bookID, userID); err != nil {
		return nil, translateErr(err, ErrBookNotFound)
	}
	return r.Summary(ctx, bookID, userID)
}

// Summary saca el conteo y el estado propio en una sola consulta: son dos datos
// de la misma tabla y separarlos serian dos viajes para pintar un boton.
//
// El viewer va con NULLIF porque el anonimo llega como cadena vacia, y comparar
// '' contra un uuid seria un error de tipo.
func (r *PostgresLikeRepository) Summary(ctx context.Context, bookID, viewerID string) (*domain.LikeSummary, error) {
	const query = `
		SELECT count(*)::int,
		       coalesce(bool_or(user_id = NULLIF($2, '')::uuid), false)
		FROM interaction.likes
		WHERE book_id = $1`

	s := &domain.LikeSummary{BookID: bookID}
	if err := r.pool.QueryRow(ctx, query, bookID, viewerID).Scan(&s.Count, &s.LikedByMe); err != nil {
		return nil, translateErr(err, ErrBookNotFound)
	}
	return s, nil
}
