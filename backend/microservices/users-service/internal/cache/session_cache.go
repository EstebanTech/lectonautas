// Package cache mantiene las sesiones activas en Valkey como cache-aside: la
// fuente de verdad es la BD, y Valkey guarda hash-de-token -> user_id con TTL
// para resolver la validacion sin pegarle a Postgres en cada request.
package cache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrMiss indica que la clave no estaba en Valkey (cache miss).
var ErrMiss = errors.New("cache miss")

const keyPrefix = "session:"

type SessionCache struct {
	client *redis.Client
}

func NewSessionCache(addr string) *SessionCache {
	return &SessionCache{
		client: redis.NewClient(&redis.Options{Addr: addr}),
	}
}

func (c *SessionCache) Ping(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}

func (c *SessionCache) Close() error {
	return c.client.Close()
}

// Set guarda hash-de-token -> userID con un TTL (2h al iniciar sesion, o el
// tiempo restante cuando se repuebla desde la BD).
func (c *SessionCache) Set(ctx context.Context, tokenHash, userID string, ttl time.Duration) error {
	return c.client.Set(ctx, keyPrefix+tokenHash, userID, ttl).Err()
}

// Get devuelve el userID asociado al hash del token, o ErrMiss si no esta.
func (c *SessionCache) Get(ctx context.Context, tokenHash string) (string, error) {
	userID, err := c.client.Get(ctx, keyPrefix+tokenHash).Result()
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
	return c.client.Del(ctx, keyPrefix+tokenHash).Err()
}
