package repository

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/domain"
)

type GenreRepository interface {
	// List devuelve el catalogo completo, ordenado por nombre.
	List(ctx context.Context) ([]*domain.Genre, error)
	// ReplaceForBook deja el libro exactamente con los generos que se le pasan:
	// borra los que sobran e inserta los que faltan, en una transaccion. Con la
	// lista vacia el libro se queda sin generos.
	ReplaceForBook(ctx context.Context, bookID string, slugs []string) error
}

type PostgresGenreRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresGenreRepository(pool *pgxpool.Pool) *PostgresGenreRepository {
	return &PostgresGenreRepository{pool: pool}
}

func (r *PostgresGenreRepository) List(ctx context.Context) ([]*domain.Genre, error) {
	const query = `SELECT slug, name FROM content.genres ORDER BY name ASC`

	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, translateErr(err, ErrGenreNotFound)
	}
	defer rows.Close()

	genres := make([]*domain.Genre, 0)
	for rows.Next() {
		g := &domain.Genre{}
		if err := rows.Scan(&g.Slug, &g.Name); err != nil {
			return nil, err
		}
		genres = append(genres, g)
	}
	return genres, rows.Err()
}

func (r *PostgresGenreRepository) ReplaceForBook(ctx context.Context, bookID string, slugs []string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := replaceBookGenres(ctx, tx, bookID, slugs); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// replaceBookGenres es el cuerpo de la operacion, sobre una transaccion ya
// abierta: asi la comparte el alta del libro, que escribe libro y generos de una
// sola vez, con el reemplazo posterior.
//
// Borra e inserta en vez de calcular la diferencia porque la lista tiene 4
// elementos como mucho: no vale la pena mas maquinaria.
func replaceBookGenres(ctx context.Context, tx pgx.Tx, bookID string, slugs []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM content.book_genres WHERE book_id = $1`, bookID); err != nil {
		return translateErr(err, ErrBookNotFound)
	}
	if len(slugs) == 0 {
		return nil
	}

	// unnest inserta las cuatro filas en un solo viaje; el ORDER BY del SELECT
	// no importa, el orden de presentacion lo pone la consulta de lectura.
	const insert = `
		INSERT INTO content.book_genres (book_id, genre)
		SELECT $1, slug FROM unnest($2::text[]) AS slug`

	if _, err := tx.Exec(ctx, insert, bookID, slugs); err != nil {
		return translateErr(err, ErrBookNotFound)
	}
	return nil
}

// attachGenres rellena Genres en los libros que se pasan, con una sola consulta
// para todos: hacerlo libro por libro seria un N+1 en cada listado.
//
// Un libro sin generos se queda con la lista vacia, no en nil, para que el JSON
// que se cachea y el que sale por la API sean siempre un array.
func attachGenres(ctx context.Context, q querier, books []*domain.Book) error {
	if len(books) == 0 {
		return nil
	}

	byID := make(map[string]*domain.Book, len(books))
	ids := make([]string, 0, len(books))
	for _, b := range books {
		b.Genres = make([]*domain.Genre, 0, domain.GenreMaxPerBook)
		byID[b.ID] = b
		ids = append(ids, b.ID)
	}

	const query = `
		SELECT bg.book_id::text, g.slug, g.name
		FROM content.book_genres bg
		JOIN content.genres g ON g.slug = bg.genre
		WHERE bg.book_id = ANY($1::uuid[])
		ORDER BY g.name ASC`

	rows, err := q.Query(ctx, query, ids)
	if err != nil {
		return translateErr(err, ErrBookNotFound)
	}
	defer rows.Close()

	for rows.Next() {
		var bookID string
		g := &domain.Genre{}
		if err := rows.Scan(&bookID, &g.Slug, &g.Name); err != nil {
			return err
		}
		if b, ok := byID[bookID]; ok {
			b.Genres = append(b.Genres, g)
		}
	}
	return rows.Err()
}
