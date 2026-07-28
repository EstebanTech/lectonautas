package service

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/cache"
	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/domain"
	libraryv1 "github.com/EstebanTech/lectonautas/backend/microservices/library-service/proto/library/v1"
)

// cachedBookDetail es lo que se guarda en Valkey para GetBook: el libro con la
// lista de capitulos que corresponde al alcance de quien lo pidio.
type cachedBookDetail struct {
	Book     *domain.Book      `json:"book"`
	Chapters []*domain.Chapter `json:"chapters"`
}

// cachedBookList es lo que se guarda para ListBooks: la pagina y el total, que
// no se puede recalcular desde la pagina sola.
type cachedBookList struct {
	Books []*domain.Book `json:"books"`
	Total int32          `json:"total"`
}

// CreateBook crea el libro vacio. Los capitulos, incluido el primero, se
// agregan despues con CreateChapter: son un recurso propio y se editan aparte.
func (s *LibraryService) CreateBook(ctx context.Context, req *libraryv1.CreateBookRequest) (*libraryv1.BookResponse, error) {
	authorID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}

	title, err := titleField.Required(req.GetTitle())
	if err != nil {
		return nil, err
	}
	description, err := descriptionField.Optional(req.GetDescription())
	if err != nil {
		return nil, err
	}
	coverURL, err := coverURLField.Optional(req.GetCoverUrl())
	if err != nil {
		return nil, err
	}
	// Los generos son opcionales al crear: el libro puede nacer sin ninguno y
	// ponerselos despues con SetBookGenres.
	genres, err := validateGenres(req.GetGenres())
	if err != nil {
		return nil, err
	}

	bookStatus := domain.BookStatusDraft
	if req.GetStatus() != "" {
		if bookStatus, err = validateBookStatus(req.GetStatus()); err != nil {
			return nil, err
		}
	}
	// Un libro recien creado no tiene capitulos, asi que no puede nacer
	// publicado sin romper la invariante. Se rechaza en vez de degradarlo a
	// borrador en silencio: el autor pidio publicar y tiene que enterarse de
	// que falta el contenido.
	if bookStatus == domain.BookStatusPublished {
		return nil, status.Error(codes.FailedPrecondition,
			"a new book has no chapters yet and cannot be published")
	}

	created, err := s.books.Create(ctx, &domain.Book{
		AuthorID:    authorID,
		Title:       title,
		Description: description,
		CoverURL:    coverURL,
		Status:      bookStatus,
	}, genres)
	if err != nil {
		return nil, mapRepoErr(err, "failed to create book")
	}

	s.cache.Invalidate(ctx)

	return &libraryv1.BookResponse{Book: bookToProto(created)}, nil
}

// ListBooks es el listado publico y solo eso: siempre filtra por published,
// haya token o no. La vista del autor sobre su propia obra vive en ListMyBooks,
// separada a proposito — un endpoint, una regla de visibilidad.
func (s *LibraryService) ListBooks(ctx context.Context, req *libraryv1.ListBooksRequest) (*libraryv1.ListBooksResponse, error) {
	// Se valida antes de la consulta: un UUID malformado hace que Postgres
	// aborte y el repositorio lo traduciria a "book not found", que en un
	// listado no significa nada.
	authorID, err := optionalUUIDFilter("author_id", req.GetAuthorId())
	if err != nil {
		return nil, err
	}

	// Se rechaza en vez de ignorarse: pedir draft aqui y recibir published sin
	// aviso haria pensar que el autor no tiene borradores.
	if st := req.GetStatus(); st != "" && st != domain.BookStatusPublished {
		return nil, status.Error(codes.InvalidArgument,
			"this listing only returns published books; use GET /v1/books/mine to see your own drafts")
	}

	// No se resuelve el token: este listado no lo necesita para nada. Filtra
	// siempre por published y el chapter_count es el mismo numero para todos.
	filter := domain.BookFilter{
		AuthorID: authorID,
		Search:   req.GetSearch(),
		Genre:    optionalGenreFilter(req.GetGenre()),
		Status:   domain.BookStatusPublished,
	}
	filter.Page, filter.PageSize = normalizePage(req.GetPage(), req.GetPageSize())

	books, total, err := s.queryBooks(ctx, filter, scopePublic)
	if err != nil {
		return nil, err
	}

	return &libraryv1.ListBooksResponse{
		Books:    booksToProto(books),
		Total:    total,
		Page:     filter.Page,
		PageSize: filter.PageSize,
	}, nil
}

// ListMyBooks devuelve los libros del autor autenticado en cualquier estado y
// TODOS de una vez: este listado no se pagina.
//
// Es la unica lectura sin tope de tamano del servicio, y se lo permite porque
// lo que devuelve esta acotado por naturaleza: son los libros que una persona
// escribio, no un catalogo que crece con los usuarios. El listado publico, que
// si crece sin limite, sigue paginado.
//
// El author_id sale del token y no de la peticion: no hay forma de pedir la
// obra inedita de otro, ni equivocandose ni a proposito. Lo que el token
// demuestra es que la sesion existe y sigue vigente (lo resuelve user-service,
// unico dueno de esa tabla); si alguien roba un token valido, este endpoint no
// puede distinguirlo del legitimo — de eso se encarga el logout, que revoca la
// sesion.
func (s *LibraryService) ListMyBooks(ctx context.Context, req *libraryv1.ListMyBooksRequest) (*libraryv1.ListMyBooksResponse, error) {
	authorID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}

	// Sin Page ni PageSize: el cero es lo que le dice al repositorio que saque
	// la consulta sin LIMIT.
	filter := domain.BookFilter{
		AuthorID: authorID,
		Search:   req.GetSearch(),
		Genre:    optionalGenreFilter(req.GetGenre()),
	}

	// Sobre lo propio status es un filtro libre; vacio trae los tres estados.
	if req.GetStatus() != "" {
		if filter.Status, err = validateBookStatus(req.GetStatus()); err != nil {
			return nil, err
		}
	}

	books, total, err := s.queryBooks(ctx, filter, scopeOwner)
	if err != nil {
		return nil, err
	}

	return &libraryv1.ListMyBooksResponse{
		Books: booksToProto(books),
		Total: total,
	}, nil
}

// queryBooks es el cuerpo comun de los dos listados: consulta cacheada. Cada
// uno le da forma a su respuesta por su cuenta, porque el publico lleva los
// datos de la pagina y el propio no.
//
// El scope entra en la clave de cache para que la version del autor y la
// publica nunca se pisen; la paginacion tambien, y en el listado sin paginar
// entra como el cero que la representa.
func (s *LibraryService) queryBooks(ctx context.Context, filter domain.BookFilter, scope string) ([]*domain.Book, int32, error) {
	var cached cachedBookList
	key, hit := s.cache.Get(ctx, &cached, "books",
		scope, filter.AuthorID, filter.Status, filter.Search, filter.Genre,
		cache.FormatInt(filter.Page), cache.FormatInt(filter.PageSize))
	if hit {
		return cached.Books, cached.Total, nil
	}

	books, total, err := s.books.List(ctx, filter)
	if err != nil {
		return nil, 0, mapRepoErr(err, "failed to list books")
	}

	s.cache.Set(ctx, key, cachedBookList{Books: books, Total: total})

	return books, total, nil
}

// GetBook devuelve el libro con sus capitulos. Un lector ajeno solo ve el libro
// si esta publicado, y de sus capitulos solo los publicados; el autor lo ve
// todo.
func (s *LibraryService) GetBook(ctx context.Context, req *libraryv1.GetBookRequest) (*libraryv1.BookDetailResponse, error) {
	id, err := requiredID("id", req.GetId())
	if err != nil {
		return nil, err
	}

	callerID, err := s.auth.Optional(ctx)
	if err != nil {
		return nil, err
	}

	// Se prueba primero la version publica del detalle. Si esta cacheada y quien
	// pregunta no es su autor, esa es exactamente la que le toca y no hace falta
	// tocar Postgres ni siquiera para comprobar la visibilidad: una entrada
	// publica solo se escribe cuando el libro estaba publicado, y cualquier
	// escritura posterior —incluida la que lo despublicaria— habria movido el
	// contador de version que la clave lleva dentro, dejandola inalcanzable.
	//
	// Antes la visibilidad se resolvia siempre contra la BD ANTES de mirar el
	// cache, asi que la lectura mas frecuente del servicio (un lector abriendo
	// un libro ajeno) pagaba una consulta aunque el cache estuviera caliente.
	var pub cachedBookDetail
	pubKey, hit := s.cache.Get(ctx, &pub, "book", id, scopePublic)
	if hit && pub.Book != nil && pub.Book.AuthorID != callerID {
		return bookDetail(pub.Book, pub.Chapters), nil
	}

	// O pregunta el autor por lo suyo, o no habia nada cacheado: en ambos casos
	// hay que resolver la visibilidad contra la BD.
	book, isAuthor, err := s.visibleBook(ctx, id, callerID)
	if err != nil {
		return nil, err
	}

	key := pubKey
	if isAuthor {
		// La vista del autor va en su propia clave, porque incluye los capitulos
		// en borrador que nadie mas puede ver.
		var own cachedBookDetail
		ownKey, ownHit := s.cache.Get(ctx, &own, "book", id, scopeOwner)
		if ownHit && own.Book != nil {
			return bookDetail(own.Book, own.Chapters), nil
		}
		key = ownKey
	}

	chapters, err := s.chapters.ListByBook(ctx, id, !isAuthor)
	if err != nil {
		return nil, mapRepoErr(err, "failed to load chapters")
	}

	s.cache.Set(ctx, key, cachedBookDetail{Book: book, Chapters: chapters})

	return bookDetail(book, chapters), nil
}

func bookDetail(book *domain.Book, chapters []*domain.Chapter) *libraryv1.BookDetailResponse {
	return &libraryv1.BookDetailResponse{
		Book:     bookToProto(book),
		Chapters: chaptersToProto(chapters),
	}
}

func (s *LibraryService) UpdateBook(ctx context.Context, req *libraryv1.UpdateBookRequest) (*libraryv1.BookResponse, error) {
	book, _, err := s.requireOwnedBook(ctx, "id", req.GetId())
	if err != nil {
		return nil, err
	}

	title, err := titleField.UpdateRequired(req.Title)
	if err != nil {
		return nil, err
	}
	description, err := descriptionField.Update(req.Description)
	if err != nil {
		return nil, err
	}
	coverURL, err := coverURLField.Update(req.CoverUrl)
	if err != nil {
		return nil, err
	}

	var bookStatus *string
	if req.Status != nil {
		st, err := validateBookStatus(*req.Status)
		if err != nil {
			return nil, err
		}
		// Publicar es el momento en que el libro queda a la vista del lector:
		// aqui es donde se exige que ya no este vacio. Un libro sin capitulos
		// se queda en borrador, no hay forma de sacarlo de ahi.
		if st == domain.BookStatusPublished {
			chapters, err := s.chapterCount(ctx, book.ID)
			if err != nil {
				return nil, err
			}
			if chapters == 0 {
				return nil, status.Error(codes.FailedPrecondition,
					"an empty book cannot be published")
			}
		}
		bookStatus = &st
	}

	updated, err := s.books.Update(ctx, &domain.BookUpdate{
		ID:          book.ID,
		Title:       title,
		Description: description,
		CoverURL:    coverURL,
		Status:      bookStatus,
	})
	if err != nil {
		return nil, mapRepoErr(err, "failed to update book")
	}

	s.cache.Invalidate(ctx)

	return &libraryv1.BookResponse{Book: bookToProto(updated)}, nil
}

// DeleteBook borra el libro entero. Sus capitulos, sus vinculos con sagas y las
// entradas que tuviera en la biblioteca de los lectores se van por CASCADE.
func (s *LibraryService) DeleteBook(ctx context.Context, req *libraryv1.DeleteBookRequest) (*libraryv1.DeleteResponse, error) {
	book, _, err := s.requireOwnedBook(ctx, "id", req.GetId())
	if err != nil {
		return nil, err
	}

	if err := s.books.Delete(ctx, book.ID); err != nil {
		return nil, mapRepoErr(err, "failed to delete book")
	}

	s.cache.Invalidate(ctx)
	s.dropInteractions(ctx, book.ID)

	return &libraryv1.DeleteResponse{Success: true}, nil
}
