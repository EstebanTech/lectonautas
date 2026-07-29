// Package cache concentra lo que los servicios guardan en Valkey. Todo lo que
// vive aquí es cache-aside: la fuente de verdad siempre es Postgres, y Valkey
// solo evita repetir la misma consulta durante un rato. Un fallo de Valkey nunca
// debe romper una petición, solo hacerla más lenta.
package cache

import (
	"context"
	"errors"

	"github.com/redis/go-redis/v9"
)

// ErrMiss indica que la clave no estaba en Valkey (cache miss). También se
// devuelve cuando el JSON guardado no se puede leer: se trata como si no
// estuviera y la BD repuebla la clave.
var ErrMiss = errors.New("cache miss")

// Client es la conexión compartida a Valkey.
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

// Redis expone la conexión cruda para el pub/sub de los eventos, que comparte
// el servidor con el cache pero no es cache: no guarda nada, solo reparte
// avisos. go-redis saca la suscripción a una conexión aparte del pool, así que
// un WebSocket escuchando no le quita conexiones a las consultas.
func (c *Client) Redis() *redis.Client {
	return c.rdb
}
