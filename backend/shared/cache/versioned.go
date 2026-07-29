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

// TTL es el techo de cuánto puede quedar viejo un dato que cambió por fuera de
// la API (una escritura directa a la BD). Las escrituras que pasan por el
// servicio invalidan al instante vía Bump.
const TTL = 15 * time.Minute

// Versioned cachea respuestas de lectura serializadas como JSON, con
// invalidación por versión.
//
// La invalidación no es por clave: toda clave lleva dentro el valor de un
// contador (`<prefijo>ver`) y cualquier escritura lo incrementa, con lo que las
// claves viejas dejan de ser alcanzables de golpe y caducan solas por TTL. Se
// hace así porque casi toda lectura cruza entidades —el detalle de un libro
// incluye sus capítulos, el de una saga sus libros, el resumen de un libro sus
// tres contadores— y rastrear qué clave ensucia cada escritura sería un mapa de
// dependencias fácil de romper. El precio es invalidar de más; a cambio, nunca
// se sirve algo viejo.
//
// El prefijo lo pone cada servicio ("lib:", "inter:", "user:") porque Valkey
// está compartido entre todos y con el rate limiting: son espacios de nombres
// separados, y cada uno tiene su propio contador de versión, así que una
// escritura de libros no invalida el cache de usuarios.
type Versioned struct {
	client     *Client
	prefix     string
	versionKey string
}

// NewVersioned crea el cache de un servicio bajo su prefijo (por ejemplo
// "lib:"). El contador de versión cuelga del mismo prefijo y no lleva TTL:
// tiene que sobrevivir a las claves que versiona.
func NewVersioned(client *Client, prefix string) *Versioned {
	return &Versioned{
		client:     client,
		prefix:     prefix,
		versionKey: prefix + "ver",
	}
}

// Key arma la clave versionada. Devuelve error si no se pudo leer el contador:
// sin versión no hay clave válida, y el llamante debe seguir contra la BD.
func (v *Versioned) Key(ctx context.Context, parts ...string) (string, error) {
	version, err := v.client.rdb.Get(ctx, v.versionKey).Result()
	if errors.Is(err, redis.Nil) {
		// Todavía nadie escribió: la versión 0 es tan válida como cualquiera.
		version = "0"
	} else if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString(v.prefix)
	sb.WriteString("v")
	sb.WriteString(version)
	for _, p := range parts {
		sb.WriteString(":")
		sb.WriteString(p)
	}
	return sb.String(), nil
}

// Get deserializa en dest lo que haya bajo key, o devuelve ErrMiss.
func (v *Versioned) Get(ctx context.Context, key string, dest any) error {
	payload, err := v.client.rdb.Get(ctx, key).Bytes()
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
func (v *Versioned) Set(ctx context.Context, key string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return v.client.rdb.Set(ctx, key, payload, TTL).Err()
}

// Bump incrementa la versión, dejando inalcanzable todo lo cacheado hasta
// ahora. Se llama después de cualquier escritura.
func (v *Versioned) Bump(ctx context.Context) error {
	return v.client.rdb.Incr(ctx, v.versionKey).Err()
}

// FormatInt es azúcar para armar claves con partes numéricas (páginas, flags).
func FormatInt(v int32) string {
	return strconv.FormatInt(int64(v), 10)
}
