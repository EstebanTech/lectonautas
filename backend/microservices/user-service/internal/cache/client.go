// Package cache concentra lo que el servicio guarda en Valkey. Todo lo que vive
// aqui es cache-aside: la fuente de verdad siempre es Postgres, y Valkey solo
// evita repetir la misma consulta durante un rato. Un fallo de Valkey nunca
// debe romper una peticion, solo hacerla mas lenta.
package cache

import (
	"context"
	"errors"

	"github.com/redis/go-redis/v9"
)

// ErrMiss indica que la clave no estaba en Valkey (cache miss).
var ErrMiss = errors.New("cache miss")

// Client es la conexion compartida a Valkey. Los caches especificos
// (SessionCache, UserCache) se construyen encima de una sola instancia para no
// abrir un pool por cada uno.
type Client struct {
	rdb *redis.Client
}

func NewClient(addr string) *Client {
	return &Client{rdb: redis.NewClient(&redis.Options{Addr: addr})}
}

func (c *Client) Ping(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}

func (c *Client) Close() error {
	return c.rdb.Close()
}
