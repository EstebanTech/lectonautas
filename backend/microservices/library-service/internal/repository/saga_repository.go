package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/domain"
)

const sagaColumns = `id::text, author_id::text, title, description, created_at, updated_at`

type SagaRepository interface {
	Create(ctx context.Context, s *domain.Saga) (*domain.Saga, error)
	GetByID(ctx context.Context, id string) (*domain.Saga, error)
	// List devuelve una pagina de sagas y el total que cumple el filtro.
	List(ctx context.Context, f domain.SagaFilter) ([]*domain.Saga, int32, error)
	Update(ctx context.Context, upd *domain.SagaUpdate) (*domain.Saga, error)
	Delete(ctx context.Context, id string) error
	// ListBooks devuelve los libros de la saga ordenados por su position
	// dentro de ella.
	ListBooks(ctx context.Context, sagaID string) ([]*domain.Book, error)
	AddBook(ctx context.Context, sagaID, bookID string, position int32) error
	RemoveBook(ctx context.Context, sagaID, bookID string) error
	ReorderBooks(ctx context.Context, sagaID string, bookIDs []string) error
}

type PostgresSagaRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresSagaRepository(pool *pgxpool.Pool) *PostgresSagaRepository {
	return &PostgresSagaRepository{pool: pool}
}

func (r *PostgresSagaRepository) Create(ctx context.Context, s *domain.Saga) (*domain.Saga, error) {
	const query = `
		INSERT INTO content.sagas (author_id, title, description)
		VALUES ($1, $2, $3)
		RETURNING ` + sagaColumns

	out, err := scanSaga(r.pool.QueryRow(ctx, query, s.AuthorID, s.Title, s.Description))
	if err != nil {
		return nil, translateErr(err, ErrSagaNotFound)
	}
	return out, nil
}

func (r *PostgresSagaRepository) GetByID(ctx context.Context, id string) (*domain.Saga, error) {
	const query = `SELECT ` + sagaColumns + ` FROM content.sagas WHERE id = $1`

	out, err := scanSaga(r.pool.QueryRow(ctx, query, id))
	if err != nil {
		return nil, translateErr(err, ErrSagaNotFound)
	}
	return out, nil
}

func (r *PostgresSagaRepository) List(ctx context.Context, f domain.SagaFilter) ([]*domain.Saga, int32, error) {
	where := []string{"1 = 1"}
	args := []any{}

	add := func(clause string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}

	if f.AuthorID != "" {
		add("author_id = $%d", f.AuthorID)
	}
	if f.Search != "" {
		add("title ILIKE '%%' || $%d || '%%'", f.Search)
	}
	clause := strings.Join(where, " AND ")

	var total int32
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM content.sagas WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, translateErr(err, ErrSagaNotFound)
	}
	if total == 0 {
		return []*domain.Saga{}, 0, nil
	}

	offset := (f.Page - 1) * f.PageSize
	args = append(args, f.PageSize, offset)
	query := fmt.Sprintf(
		`SELECT `+sagaColumns+` FROM content.sagas WHERE %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d`,
		clause, len(args)-1, len(args),
	)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, translateErr(err, ErrSagaNotFound)
	}
	defer rows.Close()

	sagas := make([]*domain.Saga, 0)
	for rows.Next() {
		sg, err := scanSaga(rows)
		if err != nil {
			return nil, 0, err
		}
		sagas = append(sagas, sg)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	return sagas, total, nil
}

func (r *PostgresSagaRepository) Update(ctx context.Context, upd *domain.SagaUpdate) (*domain.Saga, error) {
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

	query := `UPDATE content.sagas SET ` + strings.Join(sets, ", ") + ` WHERE id = $1 RETURNING ` + sagaColumns

	out, err := scanSaga(r.pool.QueryRow(ctx, query, args...))
	if err != nil {
		return nil, translateErr(err, ErrSagaNotFound)
	}
	return out, nil
}

// Delete borra la saga; las filas de saga_books se van por CASCADE. Los libros
// en si no se tocan: pertenecer a una saga es opcional.
func (r *PostgresSagaRepository) Delete(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM content.sagas WHERE id = $1`, id)
	if err != nil {
		return translateErr(err, ErrSagaNotFound)
	}
	if tag.RowsAffected() == 0 {
		return ErrSagaNotFound
	}
	return nil
}

func (r *PostgresSagaRepository) ListBooks(ctx context.Context, sagaID string) ([]*domain.Book, error) {
	const query = `
		SELECT b.id::text, b.author_id::text, b.title, b.description, b.cover_url, b.status, b.created_at, b.updated_at
		FROM content.saga_books sb
		JOIN content.books b ON b.id = sb.book_id
		WHERE sb.saga_id = $1
		ORDER BY sb.position ASC`

	rows, err := r.pool.Query(ctx, query, sagaID)
	if err != nil {
		return nil, translateErr(err, ErrSagaNotFound)
	}
	defer rows.Close()

	books := make([]*domain.Book, 0)
	for rows.Next() {
		b, err := scanBook(rows)
		if err != nil {
			return nil, err
		}
		books = append(books, b)
	}
	return books, rows.Err()
}

// AddBook vincula el libro a la saga. Con position 0 lo agrega al final.
func (r *PostgresSagaRepository) AddBook(ctx context.Context, sagaID, bookID string, position int32) error {
	const query = `
		INSERT INTO content.saga_books (saga_id, book_id, position)
		VALUES (
			$1, $2,
			CASE WHEN $3::int > 0 THEN $3::int
			     ELSE (SELECT COALESCE(max(position), 0) + 1 FROM content.saga_books WHERE saga_id = $1)
			END
		)`

	if _, err := r.pool.Exec(ctx, query, sagaID, bookID, position); err != nil {
		return translateErr(err, ErrSagaNotFound)
	}
	return nil
}

func (r *PostgresSagaRepository) RemoveBook(ctx context.Context, sagaID, bookID string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM content.saga_books WHERE saga_id = $1 AND book_id = $2`, sagaID, bookID)
	if err != nil {
		return translateErr(err, ErrBookNotFound)
	}
	if tag.RowsAffected() == 0 {
		return ErrBookNotFound
	}
	return nil
}

// ReorderBooks reasigna position 1..N segun el orden de bookIDs. A diferencia
// de los capitulos no necesita la pasada por negativos: la PK de saga_books es
// (saga_id, book_id), no incluye position, asi que no hay UNIQUE que violar.
func (r *PostgresSagaRepository) ReorderBooks(ctx context.Context, sagaID string, bookIDs []string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM content.saga_books WHERE saga_id = $1`, sagaID).Scan(&total); err != nil {
		return translateErr(err, ErrSagaNotFound)
	}
	if total != len(bookIDs) {
		return ErrReorderMismatch
	}

	for i, bookID := range bookIDs {
		tag, err := tx.Exec(ctx,
			`UPDATE content.saga_books SET position = $1 WHERE saga_id = $2 AND book_id = $3`,
			i+1, sagaID, bookID)
		if err != nil {
			return translateErr(err, ErrBookNotFound)
		}
		if tag.RowsAffected() == 0 {
			return ErrReorderMismatch
		}
	}

	return tx.Commit(ctx)
}

func scanSaga(s scanner) (*domain.Saga, error) {
	sg := &domain.Saga{}
	err := s.Scan(&sg.ID, &sg.AuthorID, &sg.Title, &sg.Description, &sg.CreatedAt, &sg.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return sg, nil
}
