package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrBookNotFound    = errors.New("book not found")
	ErrChapterNotFound = errors.New("chapter not found")
	ErrSagaNotFound    = errors.New("saga not found")
	// Alguno de los slugs de genero no esta en el catalogo.
	ErrGenreNotFound = errors.New("genre not found")

	// El libro ya estaba en la saga (PK compuesta saga_id + book_id).
	ErrBookAlreadyInSaga = errors.New("book already in saga")
	// Se intento pasar del tope de generos por libro.
	ErrTooManyGenres = errors.New("too many genres for a book")
	// Dos capitulos no pueden compartir position dentro de un libro.
	ErrPositionTaken = errors.New("position already taken")
	// El conjunto enviado a un reorder no coincide con el real.
	ErrReorderMismatch = errors.New("reorder list does not match the current items")
)

// scanner cubre tanto pgx.Row (QueryRow) como pgx.Rows (Query), que exponen la
// misma firma de Scan.
type scanner interface {
	Scan(dest ...any) error
}

// querier es lo que comparten *pgxpool.Pool y pgx.Tx: sirve para las consultas
// que corren tanto sueltas como dentro de una transaccion ya abierta.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// notFound es el error de "no existe" que corresponde a cada consulta; se pasa
// a translateErr porque el driver no distingue de que tabla vino el ErrNoRows.
func translateErr(err error, notFound error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		// invalid_text_representation: el id recibido no es un UUID valido,
		// asi que no puede corresponder a ninguna fila.
		case pgErr.Code == "22P02":
			return notFound
		// foreign_key_violation contra el catalogo: el slug de genero no existe.
		// Va antes del caso general de 23503, que asume que lo que falta es el
		// libro.
		case pgErr.Code == "23503" && pgErr.ConstraintName == "book_genres_genre_fkey":
			return ErrGenreNotFound
		// foreign_key_violation: el book_id/saga_id referenciado no existe.
		case pgErr.Code == "23503":
			return ErrBookNotFound
		// check_violation levantado por el trigger que limita los generos por
		// libro. Se mira el nombre y no solo el codigo: los CHECK de status
		// tambien son 23514 y no tienen nada que ver.
		case pgErr.Code == "23514" && pgErr.ConstraintName == "book_genres_max_per_book":
			return ErrTooManyGenres
		case pgErr.Code == "23505" && pgErr.ConstraintName == "chapters_book_id_position_key":
			return ErrPositionTaken
		case pgErr.Code == "23505" && pgErr.ConstraintName == "saga_books_pkey":
			return ErrBookAlreadyInSaga
		}
	}

	return err
}

func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}
