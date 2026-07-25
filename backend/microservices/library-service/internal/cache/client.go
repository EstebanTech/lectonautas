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

// ErrMiss indica que la clave no estaba en Valkey (cache miss). Tambien se
// devuelve cuando el JSON guardado no se puede leer: se trata como si no
// estuviera y la BD repuebla la clave.
var ErrMiss = errors.New("cache miss")

// Client es la conexion compartida a Valkey.
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
