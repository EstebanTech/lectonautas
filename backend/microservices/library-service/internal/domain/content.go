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

// GenreMaxPerBook es el tope de generos que puede llevar un libro. Lo valida el
// servicio y lo vuelve a exigir un trigger en la BD, que es donde esta escrito
// el mismo numero.
const GenreMaxPerBook = 4

// Genre es una entrada del catalogo de generos, que es fijo: sale de la
// migracion y no se crea por API. Slug es el identificador estable y Name la
// etiqueta que se le muestra al lector.
type Genre struct {
	Slug string
	Name string
}

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
	// PublishedAt es cuando el libro se publico por PRIMERA vez, o nil si
	// nunca lo estuvo. No se borra al despublicar ni se pisa al volver a
	// publicar: es la fecha en que la obra salio, no la del ultimo cambio de
	// estado. Pisarla dejaria que un autor se colara una y otra vez en las
	// novedades con solo despublicar y republicar.
	PublishedAt *time.Time
	// ChapterCount son los capitulos PUBLICADOS del libro, para todo el mundo
	// por igual: un borrador todavia no es parte del libro. Lo calcula la
	// consulta, no se guarda en books.
	ChapterCount int32
	// Genres son los generos del libro, entre 0 y GenreMaxPerBook. Viven en su
	// propia tabla y se traen en una consulta aparte, no columna de books.
	Genres []*Genre
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
	// PublishedAt sigue la misma regla que la del libro: la primera vez que se
	// publico, y nil si nunca.
	PublishedAt *time.Time
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

// BookFilter son los filtros de los listados de libros. Page es 1-based.
type BookFilter struct {
	Page int32
	// PageSize en 0 (o menos) pide TODO sin paginar: la consulta sale sin LIMIT
	// ni OFFSET. Es lo que usa el listado de los libros propios; el publico
	// siempre llega con un tamano ya normalizado.
	PageSize int32
	AuthorID string
	Status   string
	Search   string
	// Genre es el slug por el que se filtra, vacio si no se filtra. Un libro
	// entra si lo tiene entre los suyos.
	Genre string
}

// SagaFilter son los filtros del listado de sagas. Las sagas no tienen estado,
// asi que no hay nada que filtrar por visibilidad. Page es 1-based.
type SagaFilter struct {
	Page     int32
	PageSize int32
	AuthorID string
	Search   string
}
