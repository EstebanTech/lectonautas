package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/domain"
)

const commentColumns = `id::text, book_id::text, user_id::text, body, created_at, updated_at`

type CommentRepository interface {
	Create(ctx context.Context, c *domain.Comment) (*domain.Comment, error)
	GetByID(ctx context.Context, bookID, id string) (*domain.Comment, error)
	// ListByBook devuelve una pagina y el total de comentarios del libro, no
	// cuantos trae la pagina.
	ListByBook(ctx context.Context, f domain.CommentFilter) ([]*domain.Comment, int32, error)
	// Update solo cambia el cuerpo: de quien es y sobre que libro no se editan.
	Update(ctx context.Context, bookID, id, body string) (*domain.Comment, error)
	Delete(ctx context.Context, bookID, id string) error
	CountByBook(ctx context.Context, bookID string) (int32, error)
}

type PostgresCommentRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresCommentRepository(pool *pgxpool.Pool) *PostgresCommentRepository {
	return &PostgresCommentRepository{pool: pool}
}

func (r *PostgresCommentRepository) Create(ctx context.Context, c *domain.Comment) (*domain.Comment, error) {
	const query = `
		INSERT INTO interaction.comments (book_id, user_id, body)
		VALUES ($1, $2, $3)
		RETURNING ` + commentColumns

	out, err := scanComment(r.pool.QueryRow(ctx, query, c.BookID, c.UserID, c.Body))
	if err != nil {
		return nil, translateErr(err, ErrBookNotFound)
	}
	return out, nil
}

// GetByID exige que el comentario sea de ese libro: la ruta lleva los dos ids y
// pedir un comentario por el libro equivocado es un 404, no el comentario.
func (r *PostgresCommentRepository) GetByID(ctx context.Context, bookID, id string) (*domain.Comment, error) {
	const query = `SELECT ` + commentColumns +
		` FROM interaction.comments WHERE id = $1 AND book_id = $2`

	out, err := scanComment(r.pool.QueryRow(ctx, query, id, bookID))
	if err != nil {
		return nil, translateErr(err, ErrCommentNotFound)
	}
	return out, nil
}

func (r *PostgresCommentRepository) ListByBook(ctx context.Context, f domain.CommentFilter) ([]*domain.Comment, int32, error) {
	total, err := r.CountByBook(ctx, f.BookID)
	if err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return []*domain.Comment{}, 0, nil
	}

	const query = `SELECT ` + commentColumns + `
		FROM interaction.comments
		WHERE book_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3`

	offset := (f.Page - 1) * f.PageSize
	rows, err := r.pool.Query(ctx, query, f.BookID, f.PageSize, offset)
	if err != nil {
		return nil, 0, translateErr(err, ErrBookNotFound)
	}
	defer rows.Close()

	comments := make([]*domain.Comment, 0)
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, 0, err
		}
		comments = append(comments, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	return comments, total, nil
}

func (r *PostgresCommentRepository) Update(ctx context.Context, bookID, id, body string) (*domain.Comment, error) {
	const query = `
		UPDATE interaction.comments
		SET body = $3, updated_at = now()
		WHERE id = $1 AND book_id = $2
		RETURNING ` + commentColumns

	out, err := scanComment(r.pool.QueryRow(ctx, query, id, bookID, body))
	if err != nil {
		return nil, translateErr(err, ErrCommentNotFound)
	}
	return out, nil
}

func (r *PostgresCommentRepository) Delete(ctx context.Context, bookID, id string) error {
	const query = `DELETE FROM interaction.comments WHERE id = $1 AND book_id = $2`

	tag, err := r.pool.Exec(ctx, query, id, bookID)
	if err != nil {
		return translateErr(err, ErrCommentNotFound)
	}
	if tag.RowsAffected() == 0 {
		return ErrCommentNotFound
	}
	return nil
}

func (r *PostgresCommentRepository) CountByBook(ctx context.Context, bookID string) (int32, error) {
	const query = `SELECT count(*)::int FROM interaction.comments WHERE book_id = $1`

	var n int32
	if err := r.pool.QueryRow(ctx, query, bookID).Scan(&n); err != nil {
		return 0, translateErr(err, ErrBookNotFound)
	}
	return n, nil
}

func scanComment(s scanner) (*domain.Comment, error) {
	c := &domain.Comment{}
	err := s.Scan(&c.ID, &c.BookID, &c.UserID, &c.Body, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}
