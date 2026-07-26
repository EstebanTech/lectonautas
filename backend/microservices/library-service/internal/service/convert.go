package service

import (
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/domain"
	libraryv1 "github.com/EstebanTech/lectonautas/backend/microservices/library-service/proto/library/v1"
)

// En proto3 no hay null para los string, asi que las columnas nullables viajan
// como cadena vacia. El gateway las imprime igual (always_print_primitive_fields).
func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func bookToProto(b *domain.Book) *libraryv1.Book {
	if b == nil {
		return nil
	}
	return &libraryv1.Book{
		Id:           b.ID,
		AuthorId:     b.AuthorID,
		Title:        b.Title,
		Description:  derefString(b.Description),
		CoverUrl:     derefString(b.CoverURL),
		Status:       b.Status,
		CreatedAt:    timestamppb.New(b.CreatedAt),
		UpdatedAt:    timestamppb.New(b.UpdatedAt),
		ChapterCount: b.ChapterCount,
	}
}

func booksToProto(books []*domain.Book) []*libraryv1.Book {
	out := make([]*libraryv1.Book, 0, len(books))
	for _, b := range books {
		out = append(out, bookToProto(b))
	}
	return out
}

func chapterToProto(ch *domain.Chapter) *libraryv1.Chapter {
	if ch == nil {
		return nil
	}
	return &libraryv1.Chapter{
		Id:        ch.ID,
		BookId:    ch.BookID,
		Title:     ch.Title,
		Content:   derefString(ch.Content),
		Position:  ch.Position,
		Status:    ch.Status,
		CreatedAt: timestamppb.New(ch.CreatedAt),
		UpdatedAt: timestamppb.New(ch.UpdatedAt),
	}
}

func chaptersToProto(chapters []*domain.Chapter) []*libraryv1.Chapter {
	out := make([]*libraryv1.Chapter, 0, len(chapters))
	for _, ch := range chapters {
		out = append(out, chapterToProto(ch))
	}
	return out
}

func sagaToProto(s *domain.Saga) *libraryv1.Saga {
	if s == nil {
		return nil
	}
	return &libraryv1.Saga{
		Id:          s.ID,
		AuthorId:    s.AuthorID,
		Title:       s.Title,
		Description: derefString(s.Description),
		CreatedAt:   timestamppb.New(s.CreatedAt),
		UpdatedAt:   timestamppb.New(s.UpdatedAt),
	}
}

func sagasToProto(sagas []*domain.Saga) []*libraryv1.Saga {
	out := make([]*libraryv1.Saga, 0, len(sagas))
	for _, sg := range sagas {
		out = append(out, sagaToProto(sg))
	}
	return out
}

func savedBookToProto(sb *domain.SavedBook) *libraryv1.SavedBook {
	if sb == nil {
		return nil
	}
	return &libraryv1.SavedBook{
		Id:        sb.ID,
		UserId:    sb.UserID,
		Kind:      sb.Kind,
		CreatedAt: timestamppb.New(sb.CreatedAt),
		Book:      bookToProto(sb.Book),
	}
}
