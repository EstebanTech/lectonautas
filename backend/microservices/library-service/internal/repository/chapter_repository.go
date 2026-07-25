package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/domain"
)

const chapterColumns = `id::text, book_id::text, title, content, position, status, created_at, updated_at`

type ChapterRepository interface {
	Create(ctx context.Context, ch *domain.Chapter) (*domain.Chapter, error)
	GetByID(ctx context.Context, bookID, id string) (*domain.Chapter, error)
	// ListByBook devuelve los capitulos ordenados por position. Con
	// publishedOnly solo trae los publicados (lo que ve un lector ajeno).
	ListByBook(ctx context.Context, bookID string, publishedOnly bool) ([]*domain.Chapter, error)
	Update(ctx context.Context, upd *domain.ChapterUpdate) (*domain.Chapter, error)
	Delete(ctx context.Context, bookID, id string) error
	CountByBook(ctx context.Context, bookID string) (int, error)
	// Reorder reasigna position 1..N segun el orden de chapterIDs, que debe
	// contener exactamente los capitulos del libro.
	Reorder(ctx context.Context, bookID string, chapterIDs []string) ([]*domain.Chapter, error)
}

type PostgresChapterRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresChapterRepository(pool *pgxpool.Pool) *PostgresChapterRepository {
	return &PostgresChapterRepository{pool: pool}
}

// Create inserta el capitulo. Con Position 0 lo agrega al final: el
// COALESCE(max(position), 0) + 1 se resuelve dentro del mismo INSERT para no
// hacer dos viajes ni abrir una ventana entre leer el maximo y escribir.
func (r *PostgresChapterRepository) Create(ctx context.Context, ch *domain.Chapter) (*domain.Chapter, error) {
	const query = `
		INSERT INTO content.chapters (book_id, title, content, position, status)
		VALUES (
			$1, $2, $3,
			CASE WHEN $4::int > 0 THEN $4::int
			     ELSE (SELECT COALESCE(max(position), 0) + 1 FROM content.chapters WHERE book_id = $1)
			END,
			$5
		)
		RETURNING ` + chapterColumns

	out, err := scanChapter(r.pool.QueryRow(ctx, query, ch.BookID, ch.Title, ch.Content, ch.Position, ch.Status))
	if err != nil {
		return nil, translateErr(err, ErrChapterNotFound)
	}
	return out, nil
}

func (r *PostgresChapterRepository) GetByID(ctx context.Context, bookID, id string) (*domain.Chapter, error) {
	const query = `SELECT ` + chapterColumns + ` FROM content.chapters WHERE id = $1 AND book_id = $2`

	out, err := scanChapter(r.pool.QueryRow(ctx, query, id, bookID))
	if err != nil {
		return nil, translateErr(err, ErrChapterNotFound)
	}
	return out, nil
}

func (r *PostgresChapterRepository) ListByBook(ctx context.Context, bookID string, publishedOnly bool) ([]*domain.Chapter, error) {
	query := `SELECT ` + chapterColumns + ` FROM content.chapters WHERE book_id = $1`
	if publishedOnly {
		query += ` AND status = 'published'`
	}
	query += ` ORDER BY position ASC`

	rows, err := r.pool.Query(ctx, query, bookID)
	if err != nil {
		return nil, translateErr(err, ErrChapterNotFound)
	}
	defer rows.Close()

	chapters := make([]*domain.Chapter, 0)
	for rows.Next() {
		ch, err := scanChapter(rows)
		if err != nil {
			return nil, err
		}
		chapters = append(chapters, ch)
	}
	return chapters, rows.Err()
}

func (r *PostgresChapterRepository) Update(ctx context.Context, upd *domain.ChapterUpdate) (*domain.Chapter, error) {
	sets := []string{"updated_at = now()"}
	args := []any{upd.ID, upd.BookID}

	set := func(column string, value any) {
		args = append(args, value)
		sets = append(sets, fmt.Sprintf("%s = $%d", column, len(args)))
	}

	if upd.Title != nil {
		set("title", *upd.Title)
	}
	if upd.Content != nil {
		set("content", nullIfEmpty(*upd.Content))
	}
	if upd.Status != nil {
		set("status", *upd.Status)
	}

	query := `UPDATE content.chapters SET ` + strings.Join(sets, ", ") +
		` WHERE id = $1 AND book_id = $2 RETURNING ` + chapterColumns

	out, err := scanChapter(r.pool.QueryRow(ctx, query, args...))
	if err != nil {
		return nil, translateErr(err, ErrChapterNotFound)
	}
	return out, nil
}

func (r *PostgresChapterRepository) Delete(ctx context.Context, bookID, id string) error {
	const query = `DELETE FROM content.chapters WHERE id = $1 AND book_id = $2`

	tag, err := r.pool.Exec(ctx, query, id, bookID)
	if err != nil {
		return translateErr(err, ErrChapterNotFound)
	}
	if tag.RowsAffected() == 0 {
		return ErrChapterNotFound
	}
	return nil
}

func (r *PostgresChapterRepository) CountByBook(ctx context.Context, bookID string) (int, error) {
	const query = `SELECT count(*) FROM content.chapters WHERE book_id = $1`

	var n int
	if err := r.pool.QueryRow(ctx, query, bookID).Scan(&n); err != nil {
		return 0, translateErr(err, ErrBookNotFound)
	}
	return n, nil
}

// Reorder escribe las posiciones nuevas en una transaccion. Va en dos pasadas
// porque (book_id, position) es UNIQUE: la primera manda todo a posiciones
// negativas (libres por definicion) y la segunda pone las definitivas, para no
// chocar con las filas que aun no se movieron.
func (r *PostgresChapterRepository) Reorder(ctx context.Context, bookID string, chapterIDs []string) ([]*domain.Chapter, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// El conjunto enviado tiene que ser exactamente el del libro: si sobra o
	// falta alguno, el reorder dejaria capitulos sin posicion coherente.
	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM content.chapters WHERE book_id = $1`, bookID).Scan(&total); err != nil {
		return nil, translateErr(err, ErrBookNotFound)
	}
	if total != len(chapterIDs) {
		return nil, ErrReorderMismatch
	}

	for i, id := range chapterIDs {
		tag, err := tx.Exec(ctx,
			`UPDATE content.chapters SET position = $1, updated_at = now() WHERE id = $2 AND book_id = $3`,
			-(i + 1), id, bookID)
		if err != nil {
			return nil, translateErr(err, ErrChapterNotFound)
		}
		if tag.RowsAffected() == 0 {
			// El id no pertenece a este libro (o no existe).
			return nil, ErrReorderMismatch
		}
	}

	if _, err := tx.Exec(ctx,
		`UPDATE content.chapters SET position = -position WHERE book_id = $1 AND position < 0`, bookID); err != nil {
		return nil, translateErr(err, ErrChapterNotFound)
	}

	rows, err := tx.Query(ctx, `SELECT `+chapterColumns+` FROM content.chapters WHERE book_id = $1 ORDER BY position ASC`, bookID)
	if err != nil {
		return nil, translateErr(err, ErrChapterNotFound)
	}
	chapters := make([]*domain.Chapter, 0, len(chapterIDs))
	for rows.Next() {
		ch, err := scanChapter(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		chapters = append(chapters, ch)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return chapters, nil
}

func scanChapter(s scanner) (*domain.Chapter, error) {
	ch := &domain.Chapter{}
	err := s.Scan(&ch.ID, &ch.BookID, &ch.Title, &ch.Content, &ch.Position, &ch.Status, &ch.CreatedAt, &ch.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return ch, nil
}
