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
	// ChapterCount son los capitulos visibles para quien pidio el libro: todos
	// si es el autor, solo los publicados si es cualquier otro. Lo calcula la
	// consulta, no se guarda en books.
	ChapterCount int32
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
	// ViewerID es quien pide el listado, vacio si no vino token. No filtra
	// nada: decide de que libros se cuentan tambien los capitulos en borrador
	// (los suyos).
	ViewerID string
}

// SagaFilter son los filtros del listado de sagas. Las sagas no tienen estado,
// asi que no hay nada que filtrar por visibilidad. Page es 1-based.
type SagaFilter struct {
	Page     int32
	PageSize int32
	AuthorID string
	Search   string
}
