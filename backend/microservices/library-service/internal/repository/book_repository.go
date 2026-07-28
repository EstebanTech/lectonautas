package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/domain"
)

const bookColumns = `id::text, author_id::text, title, description, cover_url, status, created_at, updated_at, published_at`

// chapterCountExpr arma la subconsulta que cuenta los capitulos del libro, en
// la misma lista de columnas: un JOIN con GROUP BY obligaria a agrupar por
// todas las columnas del libro, y una consulta por libro seria un N+1 en cada
// listado.
//
// Cuenta SOLO los publicados, y para todo el mundo por igual: un borrador
// todavia no es parte del libro, es material que el autor no ha soltado. El
// numero significa entonces una sola cosa —cuanto hay para leer— y no depende
// de quien pregunte.
//
// Ojo con no confundirlo con ChapterRepository.CountByBook, que si cuenta los
// borradores: ese sostiene otra regla distinta, la de que un libro publicado no
// puede estar vacio de filas.
//
// alias es como se nombra content.books en la consulta que la incrusta.
func chapterCountExpr(alias string) string {
	return fmt.Sprintf(
		`(SELECT count(*) FROM content.chapters ch
		  WHERE ch.book_id = %s.id AND ch.status = 'published')::int`, alias)
}

type BookRepository interface {
	// Create inserta el libro vacio con sus generos (los slugs ya validados por
	// el servicio), en una sola transaccion: un genero que no exista tiene que
	// hacer fallar el alta entera, no dejar un libro a medio configurar. Los
	// capitulos son un recurso aparte y se agregan despues, con
	// ChapterRepository.Create.
	Create(ctx context.Context, b *domain.Book, genres []string) (*domain.Book, error)
	GetByID(ctx context.Context, id string) (*domain.Book, error)
	List(ctx context.Context, f domain.BookFilter) ([]*domain.Book, int32, error)
	Update(ctx context.Context, upd *domain.BookUpdate) (*domain.Book, error)
	Delete(ctx context.Context, id string) error
	// DeleteByAuthor borra en una transaccion los libros y las sagas del
	// usuario. Lo usa la baja de cuenta.
	DeleteByAuthor(ctx context.Context, authorID string) (books, sagas int32, err error)
	// IDsByAuthor devuelve los ids de los libros del autor. La baja de cuenta
	// los necesita antes de borrarlos, para poder pedirle a interaction-service
	// que limpie lo que colgaba de ellos.
	IDsByAuthor(ctx context.Context, authorID string) ([]string, error)
}

type PostgresBookRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresBookRepository(pool *pgxpool.Pool) *PostgresBookRepository {
	return &PostgresBookRepository{pool: pool}
}

// Create no consulta el conteo: un libro recien creado no tiene capitulos, asi
// que siempre es 0 (el cero de scanBook, que aqui no lee esa columna).
func (r *PostgresBookRepository) Create(ctx context.Context, b *domain.Book, genres []string) (*domain.Book, error) {
	// El CASE es defensivo: hoy el servicio no deja crear un libro publicado
	// (nace vacio y un libro vacio no se publica), pero si algun dia lo
	// permitiera, la fecha saldria bien sin acordarse de tocar esto.
	const query = `
		INSERT INTO content.books (author_id, title, description, cover_url, status, published_at)
		VALUES ($1, $2, $3, $4, $5, CASE WHEN $5::varchar = 'published' THEN now() END)
		RETURNING ` + bookColumns

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	book, err := scanBookRow(tx.QueryRow(ctx, query, b.AuthorID, b.Title, b.Description, b.CoverURL, b.Status))
	if err != nil {
		return nil, translateErr(err, ErrBookNotFound)
	}
	if err := replaceBookGenres(ctx, tx, book.ID, genres); err != nil {
		return nil, err
	}
	// Se leen de vuelta en vez de armarlos con los slugs que llegaron: el nombre
	// que se le muestra al lector solo lo tiene el catalogo.
	if err := attachGenres(ctx, tx, []*domain.Book{book}); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return book, nil
}

func (r *PostgresBookRepository) GetByID(ctx context.Context, id string) (*domain.Book, error) {
	query := `SELECT ` + bookColumns + `, ` + chapterCountExpr("content.books") +
		` FROM content.books WHERE id = $1`

	b, err := scanBook(r.pool.QueryRow(ctx, query, id))
	if err != nil {
		return nil, translateErr(err, ErrBookNotFound)
	}
	if err := attachGenres(ctx, r.pool, []*domain.Book{b}); err != nil {
		return nil, err
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
	if f.Genre != "" {
		// EXISTS y no JOIN: un libro con varios generos no puede aparecer dos
		// veces en el listado ni contar doble en el total.
		add(`EXISTS (SELECT 1 FROM content.book_genres bg
		             WHERE bg.book_id = content.books.id AND bg.genre = $%d)`, f.Genre)
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

	listQuery := fmt.Sprintf(
		`SELECT `+bookColumns+`, `+chapterCountExpr("content.books")+
			` FROM content.books WHERE %s ORDER BY created_at DESC`,
		clause,
	)
	// PageSize en 0 pide todo: la consulta sale sin LIMIT ni OFFSET. Es el
	// listado de lo propio, donde el autor quiere su obra entera de una vez.
	if f.PageSize > 0 {
		offset := (f.Page - 1) * f.PageSize
		args = append(args, f.PageSize, offset)
		listQuery += fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	}

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

	if err := attachGenres(ctx, r.pool, books); err != nil {
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
		// La fecha de publicacion se pone en el mismo UPDATE, no en dos pasos:
		// el COALESCE hace que solo se escriba la primera vez y deja intacta la
		// que ya hubiera, sin leerla antes ni abrir una ventana entre leer y
		// escribir. Despublicar no la toca; volver a publicar tampoco la pisa.
		if *upd.Status == domain.BookStatusPublished {
			sets = append(sets, "published_at = COALESCE(published_at, now())")
		}
	}

	query := `UPDATE content.books SET ` + strings.Join(sets, ", ") +
		` WHERE id = $1 RETURNING ` + bookColumns + `, ` + chapterCountExpr("content.books")

	b, err := scanBook(r.pool.QueryRow(ctx, query, args...))
	if err != nil {
		return nil, translateErr(err, ErrBookNotFound)
	}
	// Update no toca los generos (eso es SetBookGenres), pero el libro que
	// devuelve tiene que venir completo igual que el de cualquier otra lectura.
	if err := attachGenres(ctx, r.pool, []*domain.Book{b}); err != nil {
		return nil, err
	}
	return b, nil
}

// Delete borra el libro; sus capitulos, sus generos y sus vinculos con sagas se
// van por CASCADE.
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
// atras al darse de baja. Los dos DELETE son los dos sitios donde su id
// aparece; lo que cuelga de sus libros (capitulos, generos y vinculos con
// sagas) se va por CASCADE.
//
// El id no lleva FK a users, que vive en otra base: por eso esto es una
// operacion explicita y no un ON DELETE CASCADE del esquema.
func (r *PostgresBookRepository) DeleteByAuthor(ctx context.Context, authorID string) (int32, int32, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)

	books, err := tx.Exec(ctx, `DELETE FROM content.books WHERE author_id = $1`, authorID)
	if err != nil {
		return 0, 0, translateErr(err, ErrBookNotFound)
	}
	sagas, err := tx.Exec(ctx, `DELETE FROM content.sagas WHERE author_id = $1`, authorID)
	if err != nil {
		return 0, 0, translateErr(err, ErrSagaNotFound)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	return int32(books.RowsAffected()), int32(sagas.RowsAffected()), nil
}

func (r *PostgresBookRepository) IDsByAuthor(ctx context.Context, authorID string) ([]string, error) {
	const query = `SELECT id::text FROM content.books WHERE author_id = $1`

	rows, err := r.pool.Query(ctx, query, authorID)
	if err != nil {
		return nil, translateErr(err, ErrBookNotFound)
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func scanBook(s scanner) (*domain.Book, error) {
	b := &domain.Book{}
	err := s.Scan(&b.ID, &b.AuthorID, &b.Title, &b.Description, &b.CoverURL, &b.Status,
		&b.CreatedAt, &b.UpdatedAt, &b.PublishedAt, &b.ChapterCount)
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
		&b.CreatedAt, &b.UpdatedAt, &b.PublishedAt)
	if err != nil {
		return nil, err
	}
	return b, nil
}
