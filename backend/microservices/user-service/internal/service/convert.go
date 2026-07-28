package service

import (
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/domain"
	userv1 "github.com/EstebanTech/lectonautas/backend/microservices/user-service/proto/user/v1"
)

// toProto arma la vista completa, con email. Solo puede usarse en respuestas
// que van hacia el propio dueno de la cuenta: CreateUser, Login, GetCurrentUser
// y UpdateUser. Todo lo demas usa toPublicProto.
func toProto(u *domain.User) *userv1.User {
	return &userv1.User{
		Id:          u.ID,
		Email:       u.Email,
		Username:    u.Username,
		DisplayName: deref(u.DisplayName),
		AvatarUrl:   deref(u.AvatarURL),
		Bio:         deref(u.Bio),
		IsActive:    u.IsActive,
		CreatedAt:   timestamppb.New(u.CreatedAt),
		UpdatedAt:   timestamppb.New(u.UpdatedAt),
	}
}

// toPublicProto arma la vista que puede ver cualquiera. El tipo de destino no
// tiene campo email, asi que aqui no hay nada que recordar omitir.
func toPublicProto(u *domain.User) *userv1.PublicUser {
	return &userv1.PublicUser{
		Id:          u.ID,
		Username:    u.Username,
		DisplayName: deref(u.DisplayName),
		AvatarUrl:   deref(u.AvatarURL),
		Bio:         deref(u.Bio),
		CreatedAt:   timestamppb.New(u.CreatedAt),
	}
}

// toPublicProtos convierte un listado entero a la vista publica.
func toPublicProtos(users []*domain.User) []*userv1.PublicUser {
	out := make([]*userv1.PublicUser, 0, len(users))
	for _, u := range users {
		out = append(out, toPublicProto(u))
	}
	return out
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
