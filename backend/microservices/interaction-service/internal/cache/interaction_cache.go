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
	// porque Valkey esta compartido con los otros servicios y el ratelimit.
	keyPrefix = "inter:"
	// versionKey es el contador que invalida en bloque. No lleva TTL: tiene que
	// sobrevivir a las claves que versiona.
	versionKey = keyPrefix + "ver"
)

// InteractionCache cachea las respuestas de lectura serializadas como JSON.
//
// La invalidacion es por version, igual que en library-service: toda clave
// lleva dentro el valor de un contador (`inter:ver`) y cualquier escritura lo
// incrementa, con lo que las claves viejas dejan de ser alcanzables de golpe y
// caducan solas por TTL. El precio es invalidar de mas — un me gusta tira
// tambien lo cacheado de otros libros —, y aqui se paga a gusto: las escrituras
// son baratas de rehacer y lo que nunca se quiere es servir un contador viejo
// justo despues de que el lector lo movio.
type InteractionCache struct {
	client *Client
}

func NewInteractionCache(client *Client) *InteractionCache {
	return &InteractionCache{client: client}
}

// TTL es el techo de cuanto puede quedar viejo un dato que cambio por fuera de
// la API. Las escrituras que pasan por el servicio invalidan al instante.
const TTL = 15 * time.Minute

// Key arma la clave versionada. Devuelve error si no se pudo leer el contador:
// sin version no hay clave valida, y el llamante debe seguir contra la BD.
func (c *InteractionCache) Key(ctx context.Context, parts ...string) (string, error) {
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
func (c *InteractionCache) Get(ctx context.Context, key string, dest any) error {
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
func (c *InteractionCache) Set(ctx context.Context, key string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.client.rdb.Set(ctx, key, payload, TTL).Err()
}

// Bump incrementa la version, dejando inalcanzable todo lo cacheado hasta
// ahora. Se llama despues de cualquier escritura.
func (c *InteractionCache) Bump(ctx context.Context) error {
	return c.client.rdb.Incr(ctx, versionKey).Err()
}

// FormatInt es azucar para armar claves con partes numericas (paginas).
func FormatInt(v int32) string {
	return strconv.FormatInt(int64(v), 10)
}
