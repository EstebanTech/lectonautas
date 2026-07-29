// Package cache guarda en Valkey lo unico que este servicio cachea con reglas
// propias: las sesiones.
//
// Los datos de usuario NO viven aqui: van por el cache versionado compartido
// (shared/cache), igual que en library-service y interaction-service. Las
// sesiones no pueden usar ese mecanismo porque su invalidacion no es un asunto
// de frescura sino de revocacion: al cerrar sesion o dar de baja una cuenta hay
// que tirar UNA entrada concreta al instante, y un contador de version que
// invalida en bloque tiraria tambien las sesiones de todos los demas, mandando
// cada peticion del sistema a Postgres.
package cache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	sharedcache "github.com/EstebanTech/lectonautas/backend/shared/cache"
)

const sessionKeyPrefix = "session:"

// SessionCache guarda hash-de-token -> user_id para resolver la validacion del
// token sin pegarle a Postgres en cada request.
type SessionCache struct {
	rdb *redis.Client
}

func NewSessionCache(client *sharedcache.Client) *SessionCache {
	return &SessionCache{rdb: client.Redis()}
}

// Set guarda hash-de-token -> userID con un TTL de cache. Ese TTL es corto a
// proposito y no tiene que ver con cuanto dura la sesion: al vencer, el
// siguiente acceso cae a la BD (fuente de verdad) y vuelve a poblar la clave.
func (c *SessionCache) Set(ctx context.Context, tokenHash, userID string, ttl time.Duration) error {
	return c.rdb.Set(ctx, sessionKeyPrefix+tokenHash, userID, ttl).Err()
}

// Get devuelve el userID asociado al hash del token, o ErrMiss si no esta.
func (c *SessionCache) Get(ctx context.Context, tokenHash string) (string, error) {
	userID, err := c.rdb.Get(ctx, sessionKeyPrefix+tokenHash).Result()
	if errors.Is(err, redis.Nil) {
		return "", sharedcache.ErrMiss
	}
	if err != nil {
		return "", err
	}
	return userID, nil
}

// Delete borra la sesion de Valkey (usado en logout).
func (c *SessionCache) Delete(ctx context.Context, tokenHash string) error {
	return c.rdb.Del(ctx, sessionKeyPrefix+tokenHash).Err()
}
