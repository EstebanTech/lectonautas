package domain

import "time"

// Limites de la calificacion, iguales al CHECK de la migracion.
const (
	ScoreMin = 1
	ScoreMax = 5
)

// Comment es un comentario de un lector sobre un libro. UserID es de
// user-service y BookID de library-service: aqui son ids planos, y el nombre de
// quien comento lo resuelve el cliente, porque este servicio no es dueno de los
// perfiles.
type Comment struct {
	ID        string
	BookID    string
	UserID    string
	Body      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Edited dice si el comentario se toco despues de escribirlo. Se calcula en vez
// de guardarse: las dos fechas ya lo saben.
func (c *Comment) Edited() bool {
	return c.UpdatedAt.After(c.CreatedAt)
}

// LikeSummary es el estado de los me gusta de un libro para quien pregunta.
// LikedByMe solo puede ser true si vino token.
type LikeSummary struct {
	BookID    string
	Count     int32
	LikedByMe bool
}

// RatingSummary es el promedio de un libro. Average es 0 cuando Count es 0: sin
// votos no hay promedio, y es Count quien lo distingue de una nota baja.
type RatingSummary struct {
	BookID  string
	Average float64
	Count   int32
	// MyScore es el voto del que pregunta, 0 si no voto o no vino token.
	MyScore int32
}

// BookInteractions es todo lo de un libro junto, que es como lo pide su ficha.
type BookInteractions struct {
	Likes        *LikeSummary
	Rating       *RatingSummary
	CommentCount int32
}

// CommentFilter son los filtros del listado de comentarios. Page es 1-based.
type CommentFilter struct {
	BookID   string
	Page     int32
	PageSize int32
}
