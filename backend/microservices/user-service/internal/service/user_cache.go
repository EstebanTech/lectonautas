package service

import (
	"time"

	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/domain"
)

// Partes con las que se arman las claves del cache de usuarios. Van dentro de
// la clave versionada que construye cache.Aside.
const (
	userKeyPart     = "user"
	allUsersKeyPart = "all"
)

// cachedUser es lo que de verdad se serializa a Valkey. Se define aparte de
// domain.User a proposito: asi el hash de la password no puede acabar en el
// cache por accidente si alguien lo agrega a una consulta. Es la unica razon
// por la que este DTO existe, y por eso no se le agregan campos "por si acaso".
type cachedUser struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	Username    string    `json:"username"`
	DisplayName *string   `json:"display_name"`
	AvatarURL   *string   `json:"avatar_url"`
	Bio         *string   `json:"bio"`
	IsActive    bool      `json:"is_active"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toCached(u *domain.User) cachedUser {
	return cachedUser{
		ID:          u.ID,
		Email:       u.Email,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		AvatarURL:   u.AvatarURL,
		Bio:         u.Bio,
		IsActive:    u.IsActive,
		CreatedAt:   u.CreatedAt,
		UpdatedAt:   u.UpdatedAt,
	}
}

func (c cachedUser) toDomain() *domain.User {
	return &domain.User{
		ID:          c.ID,
		Email:       c.Email,
		Username:    c.Username,
		DisplayName: c.DisplayName,
		AvatarURL:   c.AvatarURL,
		Bio:         c.Bio,
		IsActive:    c.IsActive,
		CreatedAt:   c.CreatedAt,
		UpdatedAt:   c.UpdatedAt,
	}
}

func toCachedList(users []*domain.User) []cachedUser {
	list := make([]cachedUser, 0, len(users))
	for _, u := range users {
		list = append(list, toCached(u))
	}
	return list
}

func fromCachedList(list []cachedUser) []*domain.User {
	users := make([]*domain.User, 0, len(list))
	for _, cu := range list {
		users = append(users, cu.toDomain())
	}
	return users
}
