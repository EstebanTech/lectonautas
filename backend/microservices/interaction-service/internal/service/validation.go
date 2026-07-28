package service

import (
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/domain"
)

const (
	// bodyMaxLen es el tope de un comentario. No hay columna que lo imponga
	// (body es TEXT), asi que este es el unico limite: sin el, un comentario
	// podria ser de megabytes.
	bodyMaxLen = 5000

	defaultPageSize = 20
	maxPageSize     = 100
)

// requiredID valida que un identificador venga presente. Que sea o no un UUID
// real lo resuelve la capa de abajo, que traduce el error del driver a
// NotFound: para una busqueda puntual, un id con forma invalida y uno que no
// existe son lo mismo.
func requiredID(field, value string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", status.Errorf(codes.InvalidArgument, "%s is required", field)
	}
	return v, nil
}

// validateBody valida el cuerpo de un comentario. Se guarda sin espacios
// alrededor: un comentario que solo tiene espacios es un comentario vacio.
func validateBody(value string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", status.Error(codes.InvalidArgument, "body is required")
	}
	if len(v) > bodyMaxLen {
		return "", status.Errorf(codes.InvalidArgument, "body must be at most %d characters", bodyMaxLen)
	}
	return v, nil
}

func validateScore(score int32) (int32, error) {
	if score < domain.ScoreMin || score > domain.ScoreMax {
		return 0, status.Errorf(codes.InvalidArgument, "score must be between %d and %d",
			domain.ScoreMin, domain.ScoreMax)
	}
	return score, nil
}

func normalizePagination(f domain.CommentFilter) domain.CommentFilter {
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
