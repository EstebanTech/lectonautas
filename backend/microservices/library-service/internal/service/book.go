package service

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/cache"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/domain"
	libraryv1 "github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/proto/library/v1"
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

// CreateBook crea el libro junto a su primer capitulo, en una transaccion: la
// regla es que un libro nunca exista sin al menos un capitulo.
func (s *LibraryService) CreateBook(ctx context.Context, req *libraryv1.CreateBookRequest) (*libraryv1.BookResponse, error) {
	authorID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}

	title, err := requiredText("title", req.GetTitle(), titleMaxLen)
	if err != nil {
		return nil, err
	}
	description, err := optionalText("description", req.GetDescription(), descriptionMaxLen)
	if err != nil {
		return nil, err
	}
	coverURL, err := optionalText("cover_url", req.GetCoverUrl(), coverURLMaxLen)
	if err != nil {
		return nil, err
	}

	bookStatus := domain.BookStatusDraft
	if req.GetStatus() != "" {
		if bookStatus, err = validateBookStatus(req.GetStatus()); err != nil {
			return nil, err
		}
	}

	// El primer capitulo es opcional en la peticion pero no en la base: si no
	// viene, se crea igual con un titulo por defecto y sin contenido.
	chapterTitle := defaultFirstChapterTitle
	chapterContent := ""
	if fc := req.GetFirstChapter(); fc != nil {
		if fc.GetTitle() != "" {
			if chapterTitle, err = requiredText("first_chapter.title", fc.GetTitle(), titleMaxLen); err != nil {
				return nil, err
			}
		}
		chapterContent = fc.GetContent()
	}
	content, err := optionalText("first_chapter.content", chapterContent, contentMaxLen)
	if err != nil {
		return nil, err
	}

	book := &domain.Book{
		AuthorID:    authorID,
		Title:       title,
		Description: description,
		CoverURL:    coverURL,
		Status:      bookStatus,
	}
	// El capitulo inicial nace en el mismo estado que el libro para que
	// publicar un libro de una sentada no deje su unico capitulo en borrador.
	firstChapter := &domain.Chapter{
		Title:   chapterTitle,
		Content: content,
		Status:  domain.ChapterStatusDraft,
	}
	if bookStatus == domain.BookStatusPublished {
		firstChapter.Status = domain.ChapterStatusPublished
	}

	created, _, err := s.books.CreateWithFirstChapter(ctx, book, firstChapter)
	if err != nil {
		return nil, mapRepoErr(err, "failed to create book")
	}

	s.invalidate(ctx)

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

	filter := normalizePagination(domain.BookFilter{
		Page:     req.GetPage(),
		PageSize: req.GetPageSize(),
		AuthorID: authorID,
		Search:   req.GetSearch(),
		Status:   domain.BookStatusPublished,
	})

	// Se rechaza en vez de ignorarse: pedir draft aqui y recibir published sin
	// aviso haria pensar que el autor no tiene borradores.
	if st := req.GetStatus(); st != "" && st != domain.BookStatusPublished {
		return nil, status.Error(codes.InvalidArgument,
			"this listing only returns published books; use GET /v1/books/mine to see your own drafts")
	}

	return s.listBooks(ctx, filter, "pub")
}

// ListMyBooks devuelve los libros del autor autenticado en cualquier estado.
//
// El author_id sale del token y no de la peticion: no hay forma de pedir la
// obra inedita de otro, ni equivocandose ni a proposito. Lo que el token
// demuestra es que la sesion existe y sigue vigente (lo resuelve user-service,
// unico dueno de esa tabla); si alguien roba un token valido, este endpoint no
// puede distinguirlo del legitimo — de eso se encarga el logout, que revoca la
// sesion.
func (s *LibraryService) ListMyBooks(ctx context.Context, req *libraryv1.ListMyBooksRequest) (*libraryv1.ListBooksResponse, error) {
	authorID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}

	filter := normalizePagination(domain.BookFilter{
		Page:     req.GetPage(),
		PageSize: req.GetPageSize(),
		AuthorID: authorID,
		Search:   req.GetSearch(),
	})

	// Sobre lo propio status es un filtro libre; vacio trae los tres estados.
	if req.GetStatus() != "" {
		if filter.Status, err = validateBookStatus(req.GetStatus()); err != nil {
			return nil, err
		}
	}

	return s.listBooks(ctx, filter, "own")
}

// listBooks es el cuerpo comun de los dos listados. El scope entra en la clave
// de cache para que la version del autor y la publica nunca se pisen.
func (s *LibraryService) listBooks(ctx context.Context, filter domain.BookFilter, scope string) (*libraryv1.ListBooksResponse, error) {
	var cached cachedBookList
	key, hit := s.cacheGet(ctx, &cached, "books",
		scope, filter.AuthorID, filter.Status, filter.Search,
		cache.FormatInt(filter.Page), cache.FormatInt(filter.PageSize))
	if hit {
		return &libraryv1.ListBooksResponse{
			Books:    booksToProto(cached.Books),
			Total:    cached.Total,
			Page:     filter.Page,
			PageSize: filter.PageSize,
		}, nil
	}

	books, total, err := s.books.List(ctx, filter)
	if err != nil {
		return nil, mapRepoErr(err, "failed to list books")
	}

	s.cacheSet(ctx, key, cachedBookList{Books: books, Total: total})

	return &libraryv1.ListBooksResponse{
		Books:    booksToProto(books),
		Total:    total,
		Page:     filter.Page,
		PageSize: filter.PageSize,
	}, nil
}

func normalizePagination(f domain.BookFilter) domain.BookFilter {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 {
		f.PageSize = defaultPageSize
	}
	if f.PageSize > maxPageSize {
		f.PageSize = maxPageSize
	}
	return f
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

	// La visibilidad se resuelve contra la BD antes de mirar el cache: es lo
	// que decide cual de las dos versiones cacheadas corresponde servir.
	book, isAuthor, err := s.visibleBook(ctx, id, callerID)
	if err != nil {
		return nil, err
	}

	scope := "pub"
	if isAuthor {
		scope = "own"
	}
	var cached cachedBookDetail
	key, hit := s.cacheGet(ctx, &cached, "book", id, scope)
	if hit {
		return &libraryv1.BookDetailResponse{
			Book:     bookToProto(cached.Book),
			Chapters: chaptersToProto(cached.Chapters),
		}, nil
	}

	chapters, err := s.chapters.ListByBook(ctx, id, !isAuthor)
	if err != nil {
		return nil, mapRepoErr(err, "failed to load chapters")
	}

	s.cacheSet(ctx, key, cachedBookDetail{Book: book, Chapters: chapters})

	return &libraryv1.BookDetailResponse{
		Book:     bookToProto(book),
		Chapters: chaptersToProto(chapters),
	}, nil
}

func (s *LibraryService) UpdateBook(ctx context.Context, req *libraryv1.UpdateBookRequest) (*libraryv1.BookResponse, error) {
	id, err := requiredID("id", req.GetId())
	if err != nil {
		return nil, err
	}

	callerID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.ownedBook(ctx, id, callerID); err != nil {
		return nil, err
	}

	title, err := validateRequiredUpdate("title", req.Title, titleMaxLen)
	if err != nil {
		return nil, err
	}
	description, err := validateOptionalText("description", req.Description, descriptionMaxLen)
	if err != nil {
		return nil, err
	}
	coverURL, err := validateOptionalText("cover_url", req.CoverUrl, coverURLMaxLen)
	if err != nil {
		return nil, err
	}

	var bookStatus *string
	if req.Status != nil {
		st, err := validateBookStatus(*req.Status)
		if err != nil {
			return nil, err
		}
		bookStatus = &st
	}

	updated, err := s.books.Update(ctx, &domain.BookUpdate{
		ID:          id,
		Title:       title,
		Description: description,
		CoverURL:    coverURL,
		Status:      bookStatus,
	})
	if err != nil {
		return nil, mapRepoErr(err, "failed to update book")
	}

	s.invalidate(ctx)

	return &libraryv1.BookResponse{Book: bookToProto(updated)}, nil
}

// DeleteBook borra el libro entero. Sus capitulos, sus vinculos con sagas y las
// entradas que tuviera en la biblioteca de los lectores se van por CASCADE.
func (s *LibraryService) DeleteBook(ctx context.Context, req *libraryv1.DeleteBookRequest) (*libraryv1.DeleteResponse, error) {
	id, err := requiredID("id", req.GetId())
	if err != nil {
		return nil, err
	}

	callerID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.ownedBook(ctx, id, callerID); err != nil {
		return nil, err
	}

	if err := s.books.Delete(ctx, id); err != nil {
		return nil, mapRepoErr(err, "failed to delete book")
	}

	s.invalidate(ctx)

	return &libraryv1.DeleteResponse{Success: true}, nil
}
