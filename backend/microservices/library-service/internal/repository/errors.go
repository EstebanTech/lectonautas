package repository

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrBookNotFound      = errors.New("book not found")
	ErrChapterNotFound   = errors.New("chapter not found")
	ErrSagaNotFound      = errors.New("saga not found")
	ErrSavedBookNotFound = errors.New("saved book not found")

	// El libro ya estaba en la saga (PK compuesta saga_id + book_id).
	ErrBookAlreadyInSaga = errors.New("book already in saga")
	// El lector ya tenia el libro guardado en esa misma categoria.
	ErrAlreadySaved = errors.New("book already saved with this kind")
	// Dos capitulos no pueden compartir position dentro de un libro.
	ErrPositionTaken = errors.New("position already taken")
	// Un libro siempre conserva al menos un capitulo.
	ErrLastChapter = errors.New("cannot delete the last chapter of a book")
	// El conjunto enviado a un reorder no coincide con el real.
	ErrReorderMismatch = errors.New("reorder list does not match the current items")
)

// scanner cubre tanto pgx.Row (QueryRow) como pgx.Rows (Query), que exponen la
// misma firma de Scan.
type scanner interface {
	Scan(dest ...any) error
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
		// foreign_key_violation: el book_id/saga_id referenciado no existe.
		case pgErr.Code == "23503":
			return ErrBookNotFound
		case pgErr.Code == "23505" && pgErr.ConstraintName == "chapters_book_id_position_key":
			return ErrPositionTaken
		case pgErr.Code == "23505" && pgErr.ConstraintName == "saved_books_user_id_book_id_kind_key":
			return ErrAlreadySaved
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
