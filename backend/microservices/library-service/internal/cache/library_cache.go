package cache

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// keyPrefix agrupa todo lo del servicio bajo un mismo espacio de nombres,
	// porque Valkey esta compartido con user-service y el ratelimit.
	keyPrefix = "lib:"
	// versionKey es el contador que invalida en bloque. No lleva TTL: tiene que
	// sobrevivir a las claves que versiona.
	versionKey = keyPrefix + "ver"
)

// LibraryCache cachea las respuestas de lectura serializadas como JSON.
//
// La invalidacion es por version, no por clave: toda clave lleva dentro el
// valor de un contador (`lib:ver`) y cualquier escritura lo incrementa, con lo
// que las claves viejas dejan de ser alcanzables de golpe y caducan solas por
// TTL. Se hace asi porque casi toda lectura aqui cruza entidades — el detalle
// de un libro incluye sus capitulos, el de una saga sus libros, la biblioteca
// del lector hace JOIN con books — y rastrear que clave ensucia cada escritura
// seria un mapa de dependencias facil de romper. El precio es invalidar de mas;
// a cambio, nunca se sirve algo viejo.
type LibraryCache struct {
	client *Client
}

func NewLibraryCache(client *Client) *LibraryCache {
	return &LibraryCache{client: client}
}

// TTL es el techo de cuanto puede quedar viejo un dato que cambio por fuera de
// la API (escritura directa a la BD). Las escrituras que pasan por el servicio
// invalidan al instante via Bump.
const TTL = 15 * time.Minute

// Key arma la clave versionada. Devuelve error si no se pudo leer el contador:
// sin version no hay clave valida, y el llamante debe seguir contra la BD.
func (c *LibraryCache) Key(ctx context.Context, parts ...string) (string, error) {
	version, err := c.client.rdb.Get(ctx, versionKey).Result()
	if errors.Is(err, redis.Nil) {
		// Todavia nadie escribio: la version 0 es tan valida como cualquiera.
		version = "0"
	} else if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString(keyPrefix)
	sb.WriteString("v")
	sb.WriteString(version)
	for _, p := range parts {
		sb.WriteString(":")
		sb.WriteString(p)
	}
	return sb.String(), nil
}

// Get deserializa en dest lo que haya bajo key, o devuelve ErrMiss.
func (c *LibraryCache) Get(ctx context.Context, key string, dest any) error {
	payload, err := c.client.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return ErrMiss
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, dest); err != nil {
		// JSON corrupto o de un formato viejo: se trata como miss.
		return ErrMiss
	}
	return nil
}

// Set guarda value bajo key con el TTL del cache.
func (c *LibraryCache) Set(ctx context.Context, key string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.client.rdb.Set(ctx, key, payload, TTL).Err()
}

// Bump incrementa la version, dejando inalcanzable todo lo cacheado hasta
// ahora. Se llama despues de cualquier escritura.
func (c *LibraryCache) Bump(ctx context.Context) error {
	return c.client.rdb.Incr(ctx, versionKey).Err()
}

// FormatInt es azucar para armar claves con partes numericas (paginas, flags).
func FormatInt(v int32) string {
	return strconv.FormatInt(int64(v), 10)
}
