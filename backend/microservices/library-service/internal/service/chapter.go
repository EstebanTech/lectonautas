package service

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/domain"
	libraryv1 "github.com/EstebanTech/lectonautas/backend/microservices/library-service/proto/library/v1"
)

// cachedChapter es lo que se guarda en Valkey para GetChapter. Lleva el
// author_id del libro, que no esta en el capitulo: es lo que permite decidir,
// con la entrada cacheada en la mano, si a quien pregunta le corresponde esta
// version o hay que ir a la BD a resolver su alcance.
type cachedChapter struct {
	Chapter  *domain.Chapter `json:"chapter"`
	AuthorID string          `json:"author_id"`
}

func (s *LibraryService) CreateChapter(ctx context.Context, req *libraryv1.CreateChapterRequest) (*libraryv1.ChapterResponse, error) {
	book, _, err := s.requireOwnedBook(ctx, "book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}

	title, err := titleField.Required(req.GetTitle())
	if err != nil {
		return nil, err
	}
	content, err := contentField.Optional(req.GetContent())
	if err != nil {
		return nil, err
	}

	chapterStatus := domain.ChapterStatusDraft
	if req.GetStatus() != "" {
		if chapterStatus, err = validateChapterStatus(req.GetStatus()); err != nil {
			return nil, err
		}
	}
	// Nacer publicado tampoco vale si el libro no lo esta: seria colar por el
	// alta lo que UpdateChapter no deja hacer despues.
	if err := requirePublishableChapter(book, chapterStatus); err != nil {
		return nil, err
	}

	if err := requiredPosition(req.GetPosition()); err != nil {
		return nil, err
	}

	created, err := s.chapters.Create(ctx, &domain.Chapter{
		BookID:   book.ID,
		Title:    title,
		Content:  content,
		Position: req.GetPosition(),
		Status:   chapterStatus,
	})
	if err != nil {
		return nil, mapRepoErr(ctx, err, "failed to create chapter")
	}

	s.cache.Invalidate(ctx)

	return &libraryv1.ChapterResponse{Chapter: chapterToProto(created)}, nil
}

// GetChapter aplica la misma visibilidad que GetBook: el capitulo en borrador
// de otro autor responde NotFound.
func (s *LibraryService) GetChapter(ctx context.Context, req *libraryv1.GetChapterRequest) (*libraryv1.ChapterResponse, error) {
	bookID, err := requiredID("book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}
	id, err := requiredID("id", req.GetId())
	if err != nil {
		return nil, err
	}

	callerID, err := s.auth.Optional(ctx)
	if err != nil {
		return nil, err
	}

	// Mismo atajo que en GetBook, y aqui es el que mas rinde: abrir un capitulo
	// es lo que mas se hace en el servicio. Una entrada publica solo se escribe
	// cuando el libro y el capitulo estaban ambos publicados, y cualquier
	// escritura posterior habria movido la version que la clave lleva dentro,
	// asi que un acierto ya es prueba de que a un lector ajeno le toca ver esto.
	var pub cachedChapter
	pubKey, hit := s.cache.Get(ctx, &pub, "chapter", bookID, id, scopePublic)
	if hit && pub.Chapter != nil && pub.AuthorID != callerID {
		return &libraryv1.ChapterResponse{Chapter: chapterToProto(pub.Chapter)}, nil
	}

	book, isAuthor, err := s.visibleBook(ctx, bookID, callerID)
	if err != nil {
		return nil, err
	}

	key := pubKey
	if isAuthor {
		var own cachedChapter
		ownKey, ownHit := s.cache.Get(ctx, &own, "chapter", bookID, id, scopeOwner)
		if ownHit && own.Chapter != nil {
			return &libraryv1.ChapterResponse{Chapter: chapterToProto(own.Chapter)}, nil
		}
		key = ownKey
	}

	chapter, err := s.chapters.GetByID(ctx, bookID, id)
	if err != nil {
		return nil, mapRepoErr(ctx, err, "failed to load chapter")
	}
	if !isAuthor && chapter.Status != domain.ChapterStatusPublished {
		return nil, status.Error(codes.NotFound, "chapter not found")
	}

	s.cache.Set(ctx, key, cachedChapter{Chapter: chapter, AuthorID: book.AuthorID})

	return &libraryv1.ChapterResponse{Chapter: chapterToProto(chapter)}, nil
}

func (s *LibraryService) UpdateChapter(ctx context.Context, req *libraryv1.UpdateChapterRequest) (*libraryv1.ChapterResponse, error) {
	book, _, err := s.requireOwnedBook(ctx, "book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}
	id, err := requiredID("id", req.GetId())
	if err != nil {
		return nil, err
	}

	title, err := titleField.UpdateRequired(req.Title)
	if err != nil {
		return nil, err
	}
	content, err := contentField.Update(req.Content)
	if err != nil {
		return nil, err
	}

	var chapterStatus *string
	if req.Status != nil {
		st, err := validateChapterStatus(*req.Status)
		if err != nil {
			return nil, err
		}
		// Publicar exige que el libro ya este publicado. Al reves —despublicar—
		// no tiene nada que impedir: la fila sigue ahi, el libro no se vacia, y
		// es decision del autor tener un libro publicado con capitulos que
		// todavia no lo estan.
		if err := requirePublishableChapter(book, st); err != nil {
			return nil, err
		}
		chapterStatus = &st
	}

	updated, err := s.chapters.Update(ctx, &domain.ChapterUpdate{
		ID:      id,
		BookID:  book.ID,
		Title:   title,
		Content: content,
		Status:  chapterStatus,
	})
	if err != nil {
		return nil, mapRepoErr(ctx, err, "failed to update chapter")
	}

	s.cache.Invalidate(ctx)

	return &libraryv1.ChapterResponse{Chapter: chapterToProto(updated)}, nil
}

// DeleteChapter vacia el libro sin problema mientras sea un borrador. Lo que no
// permite es dejar sin nada que leer a un libro ya publicado: para eso hay que
// pasarlo antes a borrador (o archivarlo), que es una decision consciente.
func (s *LibraryService) DeleteChapter(ctx context.Context, req *libraryv1.DeleteChapterRequest) (*libraryv1.DeleteResponse, error) {
	book, _, err := s.requireOwnedBook(ctx, "book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}
	id, err := requiredID("id", req.GetId())
	if err != nil {
		return nil, err
	}

	// El capitulo tiene que existir antes de contar: si no, un id inventado
	// sobre un libro con un solo capitulo daria "es el ultimo" en vez de 404.
	if _, err := s.chapters.GetByID(ctx, book.ID, id); err != nil {
		return nil, mapRepoErr(ctx, err, "failed to load chapter")
	}

	// Un libro que no esta publicado se puede vaciar del todo; el publicado no,
	// porque quedaria a la vista sin nada dentro y sin forma de volver atras
	// mas que despublicandolo.
	if book.Status == domain.BookStatusPublished {
		chapters, err := s.chapterCount(ctx, book.ID)
		if err != nil {
			return nil, err
		}
		if chapters <= 1 {
			return nil, status.Error(codes.FailedPrecondition,
				"cannot delete the last chapter of a published book")
		}
	}

	if err := s.chapters.Delete(ctx, book.ID, id); err != nil {
		return nil, mapRepoErr(ctx, err, "failed to delete chapter")
	}

	s.cache.Invalidate(ctx)

	return &libraryv1.DeleteResponse{Success: true}, nil
}

// ReorderChapters recibe todos los capitulos del libro en el orden deseado y
// les reasigna position 1..N.
func (s *LibraryService) ReorderChapters(ctx context.Context, req *libraryv1.ReorderChaptersRequest) (*libraryv1.ReorderChaptersResponse, error) {
	book, _, err := s.requireOwnedBook(ctx, "book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}

	ids := req.GetChapterIds()
	if err := requiredIDs("chapter_ids", ids); err != nil {
		return nil, err
	}

	chapters, err := s.chapters.Reorder(ctx, book.ID, ids)
	if err != nil {
		return nil, mapRepoErr(ctx, err, "failed to reorder chapters")
	}

	s.cache.Invalidate(ctx)

	return &libraryv1.ReorderChaptersResponse{Chapters: chaptersToProto(chapters)}, nil
}
