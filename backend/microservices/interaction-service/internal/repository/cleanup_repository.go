package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Counts es lo que dejo un borrado masivo, para el log de quien lo pidio.
type Counts struct {
	Likes    int32
	Comments int32
	Ratings  int32
}

// CleanupRepository borra en bloque todo lo que cuelga de un usuario o de un
// libro. Aqui no hay CASCADE que lo haga solo: ni users ni books viven en esta
// base, asi que la limpieza llega por RPC desde el servicio dueno.
type CleanupRepository interface {
	DeleteByUser(ctx context.Context, userID string) (Counts, error)
	DeleteByBook(ctx context.Context, bookID string) (Counts, error)
}

type PostgresCleanupRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresCleanupRepository(pool *pgxpool.Pool) *PostgresCleanupRepository {
	return &PostgresCleanupRepository{pool: pool}
}

func (r *PostgresCleanupRepository) DeleteByUser(ctx context.Context, userID string) (Counts, error) {
	return r.deleteBy(ctx, "user_id", userID)
}

func (r *PostgresCleanupRepository) DeleteByBook(ctx context.Context, bookID string) (Counts, error) {
	return r.deleteBy(ctx, "book_id", bookID)
}

// deleteBy corre los tres DELETE en una transaccion: o se va todo lo del
// usuario (o del libro) o no se va nada. La columna no viene del cliente, sale
// de los dos metodos de arriba, asi que interpolarla no abre nada.
func (r *PostgresCleanupRepository) deleteBy(ctx context.Context, column, id string) (Counts, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Counts{}, err
	}
	defer tx.Rollback(ctx)

	var out Counts
	for _, t := range []struct {
		table string
		dest  *int32
	}{
		{"interaction.likes", &out.Likes},
		{"interaction.comments", &out.Comments},
		{"interaction.ratings", &out.Ratings},
	} {
		tag, err := tx.Exec(ctx, `DELETE FROM `+t.table+` WHERE `+column+` = $1`, id)
		if err != nil {
			return Counts{}, translateErr(err, ErrBookNotFound)
		}
		*t.dest = int32(tag.RowsAffected())
	}

	if err := tx.Commit(ctx); err != nil {
		return Counts{}, err
	}
	return out, nil
}
