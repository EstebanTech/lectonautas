package service

import (
	"context"

	libraryv1 "github.com/EstebanTech/lectonautas/backend/microservices/library-service/proto/library/v1"
)

// DeleteAuthorContent borra todo lo que cuelga de un usuario que se da de baja.
//
// No pide token ni comprueba propiedad, a diferencia de todo lo demas en este
// servicio: quien llama es user-service, que ya autentico al dueno de la cuenta
// antes de decidir la baja. Lo que impide que lo llame cualquiera es que no
// existe como endpoint REST y que el gateway corta la ruta gRPC (envoy.yaml),
// igual que con ValidateSession del lado de user-service.
func (s *LibraryService) DeleteAuthorContent(ctx context.Context, req *libraryv1.DeleteAuthorContentRequest) (*libraryv1.DeleteAuthorContentResponse, error) {
	userID, err := requiredID("user_id", req.GetUserId())
	if err != nil {
		return nil, err
	}

	// Los ids se leen antes de borrar: despues ya no habria de donde sacarlos, y
	// hacen falta para pedirle a interaction-service que limpie lo que otros
	// lectores dejaron en esos libros. Que alguien cree un libro justo entre las
	// dos consultas es una carrera que no preocupa: es su propia baja de cuenta.
	bookIDs, err := s.books.IDsByAuthor(ctx, userID)
	if err != nil {
		return nil, mapRepoErr(err, "failed to list author books")
	}

	books, sagas, err := s.books.DeleteByAuthor(ctx, userID)
	if err != nil {
		return nil, mapRepoErr(err, "failed to delete author content")
	}

	s.cache.Invalidate(ctx)
	s.dropInteractions(ctx, bookIDs...)

	return &libraryv1.DeleteAuthorContentResponse{
		BooksDeleted: books,
		SagasDeleted: sagas,
	}, nil
}
