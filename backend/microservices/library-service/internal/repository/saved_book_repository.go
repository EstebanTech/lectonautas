package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/domain"
)

type SavedBookRepository interface {
	Save(ctx context.Context, userID, bookID, kind string) (*domain.SavedBook, error)
	// ListByUser trae la biblioteca del lector. Con kind vacio devuelve las
	// dos categorias juntas.
	ListByUser(ctx context.Context, userID, kind string) ([]*domain.SavedBook, error)
	// Unsave quita el libro. Con kind vacio lo saca de ambas categorias.
	Unsave(ctx context.Context, userID, bookID, kind string) error
}

type PostgresSavedBookRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresSavedBookRepository(pool *pgxpool.Pool) *PostgresSavedBookRepository {
	return &PostgresSavedBookRepository{pool: pool}
}

func (r *PostgresSavedBookRepository) Save(ctx context.Context, userID, bookID, kind string) (*domain.SavedBook, error) {
	const insert = `
		INSERT INTO reader.saved_books (user_id, book_id, kind)
		VALUES ($1, $2, $3)
		RETURNING id::text, user_id::text, kind, created_at`

	sb := &domain.SavedBook{}
	err := r.pool.QueryRow(ctx, insert, userID, bookID, kind).Scan(&sb.ID, &sb.UserID, &sb.Kind, &sb.CreatedAt)
	if err != nil {
		return nil, translateErr(err, ErrBookNotFound)
	}

	// El libro se trae aparte para devolver la entrada ya completa; el INSERT
	// no puede hacer el JOIN con content.books en el mismo RETURNING.
	bookQuery := `SELECT ` + bookColumns + `, ` + chapterCountExpr("content.books", 2) +
		` FROM content.books WHERE id = $1`
	book, err := scanBook(r.pool.QueryRow(ctx, bookQuery, bookID, userID))
	if err != nil {
		return nil, translateErr(err, ErrBookNotFound)
	}
	sb.Book = book

	return sb, nil
}

// ListByUser hace el JOIN con content.books, posible porque los dos esquemas
// viven en la misma base de datos.
func (r *PostgresSavedBookRepository) ListByUser(ctx context.Context, userID, kind string) ([]*domain.SavedBook, error) {
	// El viewer del conteo es el propio lector: de sus libros ve todos los
	// capitulos y de los ajenos solo los publicados. Va como parametro propio
	// ($2) aunque lleve el mismo valor que $1: alli se compara contra un uuid y
	// aqui contra text (por el NULLIF), y Postgres no puede inferir los dos
	// tipos para el mismo parametro.
	query := `
		SELECT sb.id::text, sb.user_id::text, sb.kind, sb.created_at,
		       b.id::text, b.author_id::text, b.title, b.description, b.cover_url, b.status, b.created_at, b.updated_at,
		       ` + chapterCountExpr("b", 2) + `
		FROM reader.saved_books sb
		JOIN content.books b ON b.id = sb.book_id
		WHERE sb.user_id = $1`
	args := []any{userID, userID}

	if kind != "" {
		query += ` AND sb.kind = $3`
		args = append(args, kind)
	}
	query += ` ORDER BY sb.created_at DESC`

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, translateErr(err, ErrSavedBookNotFound)
	}
	defer rows.Close()

	saved := make([]*domain.SavedBook, 0)
	for rows.Next() {
		sb := &domain.SavedBook{Book: &domain.Book{}}
		err := rows.Scan(
			&sb.ID, &sb.UserID, &sb.Kind, &sb.CreatedAt,
			&sb.Book.ID, &sb.Book.AuthorID, &sb.Book.Title, &sb.Book.Description,
			&sb.Book.CoverURL, &sb.Book.Status, &sb.Book.CreatedAt, &sb.Book.UpdatedAt,
			&sb.Book.ChapterCount,
		)
		if err != nil {
			return nil, err
		}
		saved = append(saved, sb)
	}
	return saved, rows.Err()
}

func (r *PostgresSavedBookRepository) Unsave(ctx context.Context, userID, bookID, kind string) error {
	query := `DELETE FROM reader.saved_books WHERE user_id = $1 AND book_id = $2`
	args := []any{userID, bookID}

	if kind != "" {
		query += ` AND kind = $3`
		args = append(args, kind)
	}

	tag, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return translateErr(err, ErrSavedBookNotFound)
	}
	if tag.RowsAffected() == 0 {
		return ErrSavedBookNotFound
	}
	return nil
}
