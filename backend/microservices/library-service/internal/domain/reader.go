package domain

import "time"

// Categorias validas, iguales al CHECK de la migracion. favorite y read_later
// son independientes: el mismo libro puede estar en ambas.
const (
	SavedKindFavorite  = "favorite"
	SavedKindReadLater = "read_later"
)

// SavedBook es una entrada de la biblioteca del lector. Book viene relleno
// desde el JOIN con content.books, que es posible porque ambos esquemas viven
// en la misma base.
type SavedBook struct {
	ID        string
	UserID    string
	Kind      string
	CreatedAt time.Time
	Book      *Book
}
