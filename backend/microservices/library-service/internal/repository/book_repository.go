package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/domain"
)

const bookColumns = `id::text, author_id::text, title, description, cover_url, status, created_at, updated_at`

// chapterCountExpr arma la subconsulta que cuenta los capitulos del libro, en
// la misma lista de columnas: un JOIN con GROUP BY obligaria a agrupar por
// todas las columnas del libro, y una consulta por libro seria un N+1 en cada
// listado.
//
// Cuenta lo que el que pregunta puede ver — todos si es el autor, solo los
// publicados si es cualquier otro —, que es la misma regla que aplica GetBook a
// la lista de capitulos. alias es como se nombra content.books en esa consulta
// y viewerArg el $n que lleva el id de quien pregunta (cadena vacia si es
// anonimo, de ahi el NULLIF: comparar '' contra un uuid seria un error de
// tipo).
func chapterCountExpr(alias string, viewerArg int) string {
	return fmt.Sprintf(
		`(SELECT count(*) FROM content.chapters ch
		  WHERE ch.book_id = %[1]s.id
		    AND (ch.status = 'published' OR %[1]s.author_id = NULLIF($%[2]d, '')::uuid))::int`,
		alias, viewerArg)
}

type BookRepository interface {
	// Create inserta el libro vacio. Los capitulos son un recurso aparte y se
	// agregan despues, con ChapterRepository.Create.
	Create(ctx context.Context, b *domain.Book) (*domain.Book, error)
	// GetByID trae el libro. viewerID es quien pregunta (vacio si no vino
	// token) y solo decide si el conteo de capitulos incluye los borradores.
	GetByID(ctx context.Context, id, viewerID string) (*domain.Book, error)
	List(ctx context.Context, f domain.BookFilter) ([]*domain.Book, int32, error)
	Update(ctx context.Context, upd *domain.BookUpdate) (*domain.Book, error)
	Delete(ctx context.Context, id string) error
	// DeleteByAuthor borra en una transaccion todo lo que cuelga del usuario:
	// sus libros, sus sagas y su biblioteca de lector. Lo usa la baja de cuenta.
	DeleteByAuthor(ctx context.Context, authorID string) (books, sagas, saved int32, err error)
}

type PostgresBookRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresBookRepository(pool *pgxpool.Pool) *PostgresBookRepository {
	return &PostgresBookRepository{pool: pool}
}

// Create no consulta el conteo: un libro recien creado no tiene capitulos, asi
// que siempre es 0 (el cero de scanBook, que aqui no lee esa columna).
func (r *PostgresBookRepository) Create(ctx context.Context, b *domain.Book) (*domain.Book, error) {
	const query = `
		INSERT INTO content.books (author_id, title, description, cover_url, status)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING ` + bookColumns

	book, err := scanBookRow(r.pool.QueryRow(ctx, query, b.AuthorID, b.Title, b.Description, b.CoverURL, b.Status))
	if err != nil {
		return nil, translateErr(err, ErrBookNotFound)
	}
	return book, nil
}

func (r *PostgresBookRepository) GetByID(ctx context.Context, id, viewerID string) (*domain.Book, error) {
	query := `SELECT ` + bookColumns + `, ` + chapterCountExpr("content.books", 2) +
		` FROM content.books WHERE id = $1`

	b, err := scanBook(r.pool.QueryRow(ctx, query, id, viewerID))
	if err != nil {
		return nil, translateErr(err, ErrBookNotFound)
	}
	return b, nil
}

// List arma el WHERE a partir de los filtros ya normalizados por el servicio
// (que es quien decide si el llamante puede pedir borradores). Devuelve la
// pagina y el total de filas que cumplen el filtro, no cuantas trajo.
func (r *PostgresBookRepository) List(ctx context.Context, f domain.BookFilter) ([]*domain.Book, int32, error) {
	where := []string{"1 = 1"}
	args := []any{}

	add := func(clause string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}

	if f.AuthorID != "" {
		add("author_id = $%d", f.AuthorID)
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.Search != "" {
		// ILIKE con comodines a ambos lados: subcadena, case-insensitive.
		add("title ILIKE '%%' || $%d || '%%'", f.Search)
	}

	// Un id invalido en author_id no es un error del cliente que valga un 500:
	// simplemente no hay libros de ese autor.
	clause := strings.Join(where, " AND ")

	var total int32
	countQuery := `SELECT count(*) FROM content.books WHERE ` + clause
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, translateErr(err, ErrBookNotFound)
	}
	if total == 0 {
		return []*domain.Book{}, 0, nil
	}

	// El viewer no entra en el WHERE, solo en el conteo de capitulos, asi que
	// se agrega despues del countQuery de arriba.
	offset := (f.Page - 1) * f.PageSize
	args = append(args, f.ViewerID)
	viewerArg := len(args)
	args = append(args, f.PageSize, offset)
	listQuery := fmt.Sprintf(
		`SELECT `+bookColumns+`, `+chapterCountExpr("content.books", viewerArg)+
			` FROM content.books WHERE %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d`,
		clause, len(args)-1, len(args),
	)

	rows, err := r.pool.Query(ctx, listQuery, args...)
	if err != nil {
		return nil, 0, translateErr(err, ErrBookNotFound)
	}
	defer rows.Close()

	books := make([]*domain.Book, 0)
	for rows.Next() {
		b, err := scanBook(rows)
		if err != nil {
			return nil, 0, err
		}
		books = append(books, b)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	return books, total, nil
}

func (r *PostgresBookRepository) Update(ctx context.Context, upd *domain.BookUpdate) (*domain.Book, error) {
	sets := []string{"updated_at = now()"}
	args := []any{upd.ID}

	set := func(column string, value any) {
		args = append(args, value)
		sets = append(sets, fmt.Sprintf("%s = $%d", column, len(args)))
	}

	if upd.Title != nil {
		set("title", *upd.Title)
	}
	if upd.Description != nil {
		set("description", nullIfEmpty(*upd.Description))
	}
	if upd.CoverURL != nil {
		set("cover_url", nullIfEmpty(*upd.CoverURL))
	}
	if upd.Status != nil {
		set("status", *upd.Status)
	}

	// Aqui no hace falta la regla de visibilidad del conteo: al Update solo se
	// llega tras comprobar que quien edita es el autor, y el autor cuenta todos
	// sus capitulos.
	query := `UPDATE content.books SET ` + strings.Join(sets, ", ") +
		` WHERE id = $1 RETURNING ` + bookColumns +
		`, (SELECT count(*) FROM content.chapters ch WHERE ch.book_id = content.books.id)::int`

	b, err := scanBook(r.pool.QueryRow(ctx, query, args...))
	if err != nil {
		return nil, translateErr(err, ErrBookNotFound)
	}
	return b, nil
}

// Delete borra el libro; los capitulos y los vinculos con sagas se van por
// CASCADE, igual que las entradas de la biblioteca de los lectores.
func (r *PostgresBookRepository) Delete(ctx context.Context, id string) error {
	const query = `DELETE FROM content.books WHERE id = $1`

	tag, err := r.pool.Exec(ctx, query, id)
	if err != nil {
		return translateErr(err, ErrBookNotFound)
	}
	if tag.RowsAffected() == 0 {
		return ErrBookNotFound
	}
	return nil
}

// scanBook lee las columnas del libro mas el conteo de capitulos que agrega
// chapterCountExpr. Es el que usan todas las lecturas.
// DeleteByAuthor borra en una sola transaccion todo lo que un usuario deja
// atras al darse de baja. Los tres DELETE son los tres sitios donde su id
// aparece; lo que cuelga de sus libros (capitulos, vinculos con sagas y las
// copias que otros lectores tuvieran guardadas) se va por CASCADE.
//
// El id no lleva FK a users, que vive en otra base: por eso esto es una
// operacion explicita y no un ON DELETE CASCADE del esquema.
func (r *PostgresBookRepository) DeleteByAuthor(ctx context.Context, authorID string) (int32, int32, int32, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	defer tx.Rollback(ctx)

	// La biblioteca del lector va primero: sus filas apuntan a libros que
	// pueden ser de otros autores y no las alcanzaria el borrado de abajo.
	saved, err := tx.Exec(ctx, `DELETE FROM reader.saved_books WHERE user_id = $1`, authorID)
	if err != nil {
		return 0, 0, 0, translateErr(err, ErrSavedBookNotFound)
	}
	books, err := tx.Exec(ctx, `DELETE FROM content.books WHERE author_id = $1`, authorID)
	if err != nil {
		return 0, 0, 0, translateErr(err, ErrBookNotFound)
	}
	sagas, err := tx.Exec(ctx, `DELETE FROM content.sagas WHERE author_id = $1`, authorID)
	if err != nil {
		return 0, 0, 0, translateErr(err, ErrSagaNotFound)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, 0, 0, err
	}
	return int32(books.RowsAffected()), int32(sagas.RowsAffected()), int32(saved.RowsAffected()), nil
}

func scanBook(s scanner) (*domain.Book, error) {
	b := &domain.Book{}
	err := s.Scan(&b.ID, &b.AuthorID, &b.Title, &b.Description, &b.CoverURL, &b.Status,
		&b.CreatedAt, &b.UpdatedAt, &b.ChapterCount)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// scanBookRow lee solo las columnas de la tabla, sin el conteo. Lo usa Create,
// donde el libro nace vacio y el conteo es 0 por definicion.
func scanBookRow(s scanner) (*domain.Book, error) {
	b := &domain.Book{}
	err := s.Scan(&b.ID, &b.AuthorID, &b.Title, &b.Description, &b.CoverURL, &b.Status,
		&b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return b, nil
}
