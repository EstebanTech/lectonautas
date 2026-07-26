package service

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/domain"
	libraryv1 "github.com/EstebanTech/lectonautas/backend/microservices/library-service/proto/library/v1"
)

func (s *LibraryService) CreateChapter(ctx context.Context, req *libraryv1.CreateChapterRequest) (*libraryv1.ChapterResponse, error) {
	bookID, err := requiredID("book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}

	callerID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.ownedBook(ctx, bookID, callerID); err != nil {
		return nil, err
	}

	title, err := requiredText("title", req.GetTitle(), titleMaxLen)
	if err != nil {
		return nil, err
	}
	content, err := optionalText("content", req.GetContent(), contentMaxLen)
	if err != nil {
		return nil, err
	}

	chapterStatus := domain.ChapterStatusDraft
	if req.GetStatus() != "" {
		if chapterStatus, err = validateChapterStatus(req.GetStatus()); err != nil {
			return nil, err
		}
	}

	if req.GetPosition() < 0 {
		return nil, status.Error(codes.InvalidArgument, "position must be greater than zero")
	}

	created, err := s.chapters.Create(ctx, &domain.Chapter{
		BookID:   bookID,
		Title:    title,
		Content:  content,
		Position: req.GetPosition(),
		Status:   chapterStatus,
	})
	if err != nil {
		return nil, mapRepoErr(err, "failed to create chapter")
	}

	s.invalidate(ctx)

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
	_, isAuthor, err := s.visibleBook(ctx, bookID, callerID)
	if err != nil {
		return nil, err
	}

	scope := "pub"
	if isAuthor {
		scope = "own"
	}
	var cached domain.Chapter
	key, hit := s.cacheGet(ctx, &cached, "chapter", bookID, id, scope)
	if hit {
		return &libraryv1.ChapterResponse{Chapter: chapterToProto(&cached)}, nil
	}

	chapter, err := s.chapters.GetByID(ctx, bookID, id)
	if err != nil {
		return nil, mapRepoErr(err, "failed to load chapter")
	}
	if !isAuthor && chapter.Status != domain.ChapterStatusPublished {
		return nil, status.Error(codes.NotFound, "chapter not found")
	}

	s.cacheSet(ctx, key, chapter)

	return &libraryv1.ChapterResponse{Chapter: chapterToProto(chapter)}, nil
}

func (s *LibraryService) UpdateChapter(ctx context.Context, req *libraryv1.UpdateChapterRequest) (*libraryv1.ChapterResponse, error) {
	bookID, err := requiredID("book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}
	id, err := requiredID("id", req.GetId())
	if err != nil {
		return nil, err
	}

	callerID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.ownedBook(ctx, bookID, callerID); err != nil {
		return nil, err
	}

	title, err := validateRequiredUpdate("title", req.Title, titleMaxLen)
	if err != nil {
		return nil, err
	}
	content, err := validateOptionalText("content", req.Content, contentMaxLen)
	if err != nil {
		return nil, err
	}

	var chapterStatus *string
	if req.Status != nil {
		st, err := validateChapterStatus(*req.Status)
		if err != nil {
			return nil, err
		}
		// Despublicar un capitulo no vacia el libro: la fila sigue ahi. Es
		// decision del autor tener un libro publicado cuyos capitulos aun no
		// lo estan, asi que aqui no hay nada que impedir.
		chapterStatus = &st
	}

	updated, err := s.chapters.Update(ctx, &domain.ChapterUpdate{
		ID:      id,
		BookID:  bookID,
		Title:   title,
		Content: content,
		Status:  chapterStatus,
	})
	if err != nil {
		return nil, mapRepoErr(err, "failed to update chapter")
	}

	s.invalidate(ctx)

	return &libraryv1.ChapterResponse{Chapter: chapterToProto(updated)}, nil
}

// DeleteChapter vacia el libro sin problema mientras sea un borrador. Lo que no
// permite es dejar sin nada que leer a un libro ya publicado: para eso hay que
// pasarlo antes a borrador (o archivarlo), que es una decision consciente.
func (s *LibraryService) DeleteChapter(ctx context.Context, req *libraryv1.DeleteChapterRequest) (*libraryv1.DeleteResponse, error) {
	bookID, err := requiredID("book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}
	id, err := requiredID("id", req.GetId())
	if err != nil {
		return nil, err
	}

	callerID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}
	book, err := s.ownedBook(ctx, bookID, callerID)
	if err != nil {
		return nil, err
	}

	// El capitulo tiene que existir antes de contar: si no, un id inventado
	// sobre un libro con un solo capitulo daria "es el ultimo" en vez de 404.
	if _, err := s.chapters.GetByID(ctx, bookID, id); err != nil {
		return nil, mapRepoErr(err, "failed to load chapter")
	}

	// Un libro que no esta publicado se puede vaciar del todo; el publicado no,
	// porque quedaria a la vista sin nada dentro y sin forma de volver atras
	// mas que despublicandolo.
	if book.Status == domain.BookStatusPublished {
		chapters, err := s.chapterCount(ctx, bookID)
		if err != nil {
			return nil, err
		}
		if chapters <= 1 {
			return nil, status.Error(codes.FailedPrecondition,
				"cannot delete the last chapter of a published book")
		}
	}

	if err := s.chapters.Delete(ctx, bookID, id); err != nil {
		return nil, mapRepoErr(err, "failed to delete chapter")
	}

	s.invalidate(ctx)

	return &libraryv1.DeleteResponse{Success: true}, nil
}

// ReorderChapters recibe todos los capitulos del libro en el orden deseado y
// les reasigna position 1..N.
func (s *LibraryService) ReorderChapters(ctx context.Context, req *libraryv1.ReorderChaptersRequest) (*libraryv1.ReorderChaptersResponse, error) {
	bookID, err := requiredID("book_id", req.GetBookId())
	if err != nil {
		return nil, err
	}

	callerID, err := s.auth.Require(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.ownedBook(ctx, bookID, callerID); err != nil {
		return nil, err
	}

	ids := req.GetChapterIds()
	if len(ids) == 0 {
		return nil, status.Error(codes.InvalidArgument, "chapter_ids is required")
	}
	// Un id repetido dejaria capitulos sin posicion asignada y el conteo
	// cuadraria igual, asi que se descarta aqui.
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			return nil, status.Error(codes.InvalidArgument, "chapter_ids cannot contain empty values")
		}
		if _, dup := seen[id]; dup {
			return nil, status.Error(codes.InvalidArgument, "chapter_ids cannot contain duplicates")
		}
		seen[id] = struct{}{}
	}

	chapters, err := s.chapters.Reorder(ctx, bookID, ids)
	if err != nil {
		return nil, mapRepoErr(err, "failed to reorder chapters")
	}

	s.invalidate(ctx)

	return &libraryv1.ReorderChaptersResponse{Chapters: chaptersToProto(chapters)}, nil
}
