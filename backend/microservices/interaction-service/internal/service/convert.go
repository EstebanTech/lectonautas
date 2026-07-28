package service

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/domain"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/events"
	interactionv1 "github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/proto/interaction/v1"
)

func commentToProto(c *domain.Comment) *interactionv1.Comment {
	if c == nil {
		return nil
	}
	return &interactionv1.Comment{
		Id:        c.ID,
		BookId:    c.BookID,
		UserId:    c.UserID,
		Body:      c.Body,
		CreatedAt: timestamppb.New(c.CreatedAt),
		UpdatedAt: timestamppb.New(c.UpdatedAt),
		Edited:    c.Edited(),
	}
}

func commentsToProto(comments []*domain.Comment) []*interactionv1.Comment {
	out := make([]*interactionv1.Comment, 0, len(comments))
	for _, c := range comments {
		out = append(out, commentToProto(c))
	}
	return out
}

func likeSummaryToProto(s *domain.LikeSummary) *interactionv1.LikeSummary {
	if s == nil {
		return nil
	}
	return &interactionv1.LikeSummary{
		BookId:    s.BookID,
		Count:     s.Count,
		LikedByMe: s.LikedByMe,
	}
}

func ratingSummaryToProto(s *domain.RatingSummary) *interactionv1.RatingSummary {
	if s == nil {
		return nil
	}
	return &interactionv1.RatingSummary{
		BookId:  s.BookID,
		Average: s.Average,
		Count:   s.Count,
		MyScore: s.MyScore,
	}
}

// commentToPayload es la version del comentario que viaja por el WebSocket. Va
// aparte del proto porque el WebSocket habla JSON a mano, no gRPC: las fechas
// salen en RFC 3339, que es lo que un cliente de navegador sabe leer sin
// traduccion.
func commentToPayload(c *domain.Comment) *events.CommentPayload {
	if c == nil {
		return nil
	}
	return &events.CommentPayload{
		ID:        c.ID,
		BookID:    c.BookID,
		UserID:    c.UserID,
		Body:      c.Body,
		CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: c.UpdatedAt.UTC().Format(time.RFC3339),
		Edited:    c.Edited(),
	}
}
