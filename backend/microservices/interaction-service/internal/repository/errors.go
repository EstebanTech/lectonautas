package repository

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrCommentNotFound = errors.New("comment not found")
	ErrRatingNotFound  = errors.New("rating not found")
	// El book_id no corresponde a ningun libro; en este servicio lo levanta un
	// UUID malformado, porque que el libro exista de verdad lo dice
	// library-service, no esta base.
	ErrBookNotFound = errors.New("book not found")
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
		// invalid_text_representation: el id recibido no es un UUID valido, asi
		// que no puede corresponder a ninguna fila. Igual que en los demas
		// servicios, malformado e inexistente son lo mismo para una busqueda.
		if pgErr.Code == "22P02" {
			return notFound
		}
	}

	return err
}
