package cache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

const sessionKeyPrefix = "session:"

// SessionCache guarda hash-de-token -> user_id para resolver la validacion del
// token sin pegarle a Postgres en cada request.
type SessionCache struct {
	client *Client
}

func NewSessionCache(client *Client) *SessionCache {
	return &SessionCache{client: client}
}

// Set guarda hash-de-token -> userID con un TTL de cache. Ese TTL es corto a
// proposito y no tiene que ver con cuanto dura la sesion: al vencer, el
// siguiente acceso cae a la BD (fuente de verdad) y vuelve a poblar la clave.
func (c *SessionCache) Set(ctx context.Context, tokenHash, userID string, ttl time.Duration) error {
	return c.client.rdb.Set(ctx, sessionKeyPrefix+tokenHash, userID, ttl).Err()
}

// Get devuelve el userID asociado al hash del token, o ErrMiss si no esta.
func (c *SessionCache) Get(ctx context.Context, tokenHash string) (string, error) {
	userID, err := c.client.rdb.Get(ctx, sessionKeyPrefix+tokenHash).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrMiss
	}
	if err != nil {
		return "", err
	}
	return userID, nil
}

// Delete borra la sesion de Valkey (usado en logout).
func (c *SessionCache) Delete(ctx context.Context, tokenHash string) error {
	return c.client.rdb.Del(ctx, sessionKeyPrefix+tokenHash).Err()
}
