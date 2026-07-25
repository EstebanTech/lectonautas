package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/domain"
)

const bookColumns = `id::text, author_id::text, title, description, cover_url, status, created_at, updated_at`

type BookRepository interface {
	// CreateWithFirstChapter inserta el libro y su capitulo inicial en una
	// sola transaccion: un libro nunca queda sin capitulos.
	CreateWithFirstChapter(ctx context.Context, b *domain.Book, ch *domain.Chapter) (*domain.Book, *domain.Chapter, error)
	GetByID(ctx context.Context, id string) (*domain.Book, error)
	List(ctx context.Context, f domain.BookFilter) ([]*domain.Book, int32, error)
	Update(ctx context.Context, upd *domain.BookUpdate) (*domain.Book, error)
	Delete(ctx context.Context, id string) error
}

type PostgresBookRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresBookRepository(pool *pgxpool.Pool) *PostgresBookRepository {
	return &PostgresBookRepository{pool: pool}
}

func (r *PostgresBookRepository) CreateWithFirstChapter(ctx context.Context, b *domain.Book, ch *domain.Chapter) (*domain.Book, *domain.Chapter, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	// Rollback tras un Commit exitoso es un no-op, asi que este defer cubre
	// solo el camino de error.
	defer tx.Rollback(ctx)

	const insertBook = `
		INSERT INTO content.books (author_id, title, description, cover_url, status)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING ` + bookColumns

	book, err := scanBook(tx.QueryRow(ctx, insertBook, b.AuthorID, b.Title, b.Description, b.CoverURL, b.Status))
	if err != nil {
		return nil, nil, translateErr(err, ErrBookNotFound)
	}

	const insertChapter = `
		INSERT INTO content.chapters (book_id, title, content, position, status)
		VALUES ($1, $2, $3, 1, $4)
		RETURNING ` + chapterColumns

	chapter, err := scanChapter(tx.QueryRow(ctx, insertChapter, book.ID, ch.Title, ch.Content, ch.Status))
	if err != nil {
		return nil, nil, translateErr(err, ErrChapterNotFound)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return book, chapter, nil
}

func (r *PostgresBookRepository) GetByID(ctx context.Context, id string) (*domain.Book, error) {
	const query = `SELECT ` + bookColumns + ` FROM content.books WHERE id = $1`

	b, err := scanBook(r.pool.QueryRow(ctx, query, id))
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

	offset := (f.Page - 1) * f.PageSize
	args = append(args, f.PageSize, offset)
	listQuery := fmt.Sprintf(
		`SELECT `+bookColumns+` FROM content.books WHERE %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d`,
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

	query := `UPDATE content.books SET ` + strings.Join(sets, ", ") + ` WHERE id = $1 RETURNING ` + bookColumns

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

func scanBook(s scanner) (*domain.Book, error) {
	b := &domain.Book{}
	err := s.Scan(&b.ID, &b.AuthorID, &b.Title, &b.Description, &b.CoverURL, &b.Status, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return b, nil
}
