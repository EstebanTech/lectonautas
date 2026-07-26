package cache

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/domain"
)

const (
	userKeyPrefix = "user:"
	allUsersKey   = "users:all"
)

// UserCache guarda las respuestas de lectura de usuarios en Valkey serializadas
// como JSON. Es cache-aside con invalidacion explicita: cualquier escritura
// (create/update/delete) borra lo que quedo viejo, para no servir datos
// desactualizados durante las dos horas del TTL.
type UserCache struct {
	client *Client
}

func NewUserCache(client *Client) *UserCache {
	return &UserCache{client: client}
}

// cachedUser es lo que se serializa. Se define aparte de domain.User a
// proposito: asi el hash de la password no puede acabar en Valkey por accidente
// si alguien agrega el campo a una consulta.
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

// SetUser cachea un usuario por id.
func (c *UserCache) SetUser(ctx context.Context, u *domain.User, ttl time.Duration) error {
	payload, err := json.Marshal(toCached(u))
	if err != nil {
		return err
	}
	return c.client.rdb.Set(ctx, userKeyPrefix+u.ID, payload, ttl).Err()
}

// GetUser devuelve el usuario cacheado, o ErrMiss si no esta.
func (c *UserCache) GetUser(ctx context.Context, id string) (*domain.User, error) {
	payload, err := c.client.rdb.Get(ctx, userKeyPrefix+id).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrMiss
	}
	if err != nil {
		return nil, err
	}

	var cu cachedUser
	if err := json.Unmarshal(payload, &cu); err != nil {
		// JSON corrupto o de un formato viejo: se trata como miss y la BD
		// vuelve a poblar la clave.
		return nil, ErrMiss
	}
	return cu.toDomain(), nil
}

// SetAllUsers cachea la respuesta completa de GetAllUsers bajo una sola clave.
func (c *UserCache) SetAllUsers(ctx context.Context, users []*domain.User, ttl time.Duration) error {
	list := make([]cachedUser, 0, len(users))
	for _, u := range users {
		list = append(list, toCached(u))
	}

	payload, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return c.client.rdb.Set(ctx, allUsersKey, payload, ttl).Err()
}

// GetAllUsers devuelve el listado completo cacheado, o ErrMiss si no esta.
func (c *UserCache) GetAllUsers(ctx context.Context) ([]*domain.User, error) {
	payload, err := c.client.rdb.Get(ctx, allUsersKey).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrMiss
	}
	if err != nil {
		return nil, err
	}

	var list []cachedUser
	if err := json.Unmarshal(payload, &list); err != nil {
		return nil, ErrMiss
	}

	users := make([]*domain.User, 0, len(list))
	for _, cu := range list {
		users = append(users, cu.toDomain())
	}
	return users, nil
}

// InvalidateUser borra el usuario del cache y el listado completo, que tambien
// quedo viejo. Se llama despues de update y delete.
func (c *UserCache) InvalidateUser(ctx context.Context, id string) error {
	return c.client.rdb.Del(ctx, userKeyPrefix+id, allUsersKey).Err()
}

// InvalidateAllUsers borra solo el listado completo. Se llama al crear un
// usuario: las entradas individuales existentes siguen siendo validas.
func (c *UserCache) InvalidateAllUsers(ctx context.Context) error {
	return c.client.rdb.Del(ctx, allUsersKey).Err()
}
