package service

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/domain"
)

// Las dos vistas que puede tener un mismo recurso. Entran en la clave de cache
// para que la version del autor y la publica nunca se pisen: el autor ve sus
// borradores y quien no lo es, no.
const (
	scopePublic = "pub"
	scopeOwner  = "own"
)

// ownedBook trae el libro y verifica que el llamante sea su autor. Es el
// chequeo de propiedad que precede a toda escritura sobre libros y capitulos.
func (s *LibraryService) ownedBook(ctx context.Context, bookID, callerID string) (*domain.Book, error) {
	book, err := s.books.GetByID(ctx, bookID)
	if err != nil {
		return nil, mapRepoErr(err, "failed to load book")
	}
	if book.AuthorID != callerID {
		return nil, status.Error(codes.PermissionDenied, "only the author can modify this book")
	}
	return book, nil
}

// requireOwnedBook resuelve de una vez el preambulo que comparten todas las
// escrituras sobre un libro: validar el id, exigir sesion y comprobar que quien
// llama es el autor. Los tres pasos iban sueltos y en el mismo orden en cada
// handler, y ese orden importa —el id antes que el token, el token antes que la
// propiedad—, asi que conviene tenerlo escrito en un solo sitio.
func (s *LibraryService) requireOwnedBook(ctx context.Context, field, bookID string) (*domain.Book, string, error) {
	id, err := requiredID(field, bookID)
	if err != nil {
		return nil, "", err
	}
	callerID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, "", err
	}
	book, err := s.ownedBook(ctx, id, callerID)
	if err != nil {
		return nil, "", err
	}
	return book, callerID, nil
}

// ownedSaga hace lo mismo para las sagas.
func (s *LibraryService) ownedSaga(ctx context.Context, sagaID, callerID string) (*domain.Saga, error) {
	saga, err := s.sagas.GetByID(ctx, sagaID)
	if err != nil {
		return nil, mapRepoErr(err, "failed to load saga")
	}
	if saga.AuthorID != callerID {
		return nil, status.Error(codes.PermissionDenied, "only the author can modify this saga")
	}
	return saga, nil
}

// requireOwnedSaga es requireOwnedBook para las sagas.
func (s *LibraryService) requireOwnedSaga(ctx context.Context, field, sagaID string) (*domain.Saga, string, error) {
	id, err := requiredID(field, sagaID)
	if err != nil {
		return nil, "", err
	}
	callerID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, "", err
	}
	saga, err := s.ownedSaga(ctx, id, callerID)
	if err != nil {
		return nil, "", err
	}
	return saga, callerID, nil
}

// visibleBook aplica la regla de visibilidad de lectura: el autor ve el libro
// en cualquier estado, y cualquier otro solo si esta publicado. Un borrador
// ajeno responde NotFound, no PermissionDenied: que exista tampoco es publico.
func (s *LibraryService) visibleBook(ctx context.Context, bookID, callerID string) (*domain.Book, bool, error) {
	book, err := s.books.GetByID(ctx, bookID)
	if err != nil {
		return nil, false, mapRepoErr(err, "failed to load book")
	}

	isAuthor := callerID != "" && book.AuthorID == callerID
	if !isAuthor && book.Status != domain.BookStatusPublished {
		return nil, false, status.Error(codes.NotFound, "book not found")
	}
	return book, isAuthor, nil
}

// requirePublishableChapter exige que el libro este publicado para poder
// publicar un capitulo suyo. Publicar es "esto ya se puede leer", y en un libro
// que todavia no esta a la vista de nadie eso no significa nada: el capitulo
// quedaria marcado como publico sin serlo, y el autor creeria haber soltado algo
// que nadie puede abrir.
//
// El orden de trabajo que impone es: crear el libro, escribirle capitulos en
// borrador, publicar el libro y despues ir publicando capitulos. No se traba con
// la regla de "un libro vacio no se publica", porque para publicar el libro solo
// hace falta que TENGA capitulos, no que esten publicados.
//
// Solo mira la transicion. Un libro que se despublica conserva sus capitulos
// publicados tal cual: degradarlos en cascada seria perder el estado de trabajo
// del autor, y no hace falta para nada — un libro que no esta publicado no lo ve
// nadie mas que el, y con el sus capitulos.
func requirePublishableChapter(book *domain.Book, chapterStatus string) error {
	if chapterStatus != domain.ChapterStatusPublished {
		return nil
	}
	if book.Status != domain.BookStatusPublished {
		return status.Errorf(codes.FailedPrecondition,
			"a chapter cannot be published while the book is %s; publish the book first",
			book.Status)
	}
	return nil
}

// chapterCount cuenta los capitulos del libro, en cualquier estado. Es la
// consulta que sostiene la invariante del servicio: un libro publicado no esta
// vacio.
//
// Antes la garantia era mas fuerte (todo libro nacia con un capitulo, y no se
// podia borrar el ultimo), pero eso obligaba a crear el libro y su contenido de
// una sola vez. Ahora el libro nace vacio y solo se le exige tener contenido
// para salir del borrador. Que ese contenido este publicado o no es decision
// del autor: puede publicar la ficha del libro e ir soltando los capitulos
// despues.
func (s *LibraryService) chapterCount(ctx context.Context, bookID string) (int, error) {
	n, err := s.chapters.CountByBook(ctx, bookID)
	if err != nil {
		return 0, mapRepoErr(err, "failed to count chapters")
	}
	return n, nil
}
