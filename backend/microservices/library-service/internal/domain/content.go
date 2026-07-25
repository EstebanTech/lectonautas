package domain

import "time"

// Estados validos, iguales a los CHECK de las migraciones.
const (
	BookStatusDraft     = "draft"
	BookStatusPublished = "published"
	BookStatusArchived  = "archived"

	ChapterStatusDraft     = "draft"
	ChapterStatusPublished = "published"
)

// Book es un libro del autor. El texto no vive aqui: books no tiene columna de
// contenido, todo esta en Chapter.
type Book struct {
	ID          string
	AuthorID    string
	Title       string
	Description *string
	CoverURL    *string
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// BookUpdate describe una modificacion parcial: los campos en nil se dejan
// intactos, los demas se escriben (una cadena vacia limpia la columna).
type BookUpdate struct {
	ID          string
	Title       *string
	Description *string
	CoverURL    *string
	Status      *string
}

type Chapter struct {
	ID        string
	BookID    string
	Title     string
	Content   *string
	Position  int32
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ChapterUpdate struct {
	ID      string
	BookID  string
	Title   *string
	Content *string
	Status  *string
}

type Saga struct {
	ID          string
	AuthorID    string
	Title       string
	Description *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type SagaUpdate struct {
	ID          string
	Title       *string
	Description *string
}

// BookFilter son los filtros del listado publico de libros. Page es 1-based.
type BookFilter struct {
	Page     int32
	PageSize int32
	AuthorID string
	Status   string
	Search   string
}

// SagaFilter son los filtros del listado de sagas. Las sagas no tienen estado,
// asi que no hay nada que filtrar por visibilidad. Page es 1-based.
type SagaFilter struct {
	Page     int32
	PageSize int32
	AuthorID string
	Search   string
}
