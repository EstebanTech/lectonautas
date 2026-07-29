// Package config resuelve la configuración que todos los servicios leen igual:
// el .env global del monorepo y las variables de entorno con su cadena de
// respaldo.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
)

// rootMarker es el archivo que identifica la raíz del monorepo.
const rootMarker = "docker-compose.yml"

// LoadRootDotEnv carga el .env global de la raíz del monorepo, buscándolo hacia
// arriba desde el directorio actual (así funciona igual si se ejecuta desde la
// carpeta del servicio o desde la raíz). Devuelve la ruta cargada.
//
// godotenv no pisa variables ya definidas en el entorno, así que dentro de
// docker manda lo que inyecta el compose aunque el archivo estuviera montado.
func LoadRootDotEnv() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, rootMarker)); err == nil {
			path := filepath.Join(dir, ".env")
			if err := godotenv.Load(path); err != nil {
				return "", err
			}
			return path, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("repo root (%s) not found from working directory", rootMarker)
		}
		dir = parent
	}
}

// FirstNonEmpty devuelve el primer valor con contenido. Es lo que arma la
// cadena de respaldo de cada variable: primero el nombre que inyecta el compose
// (DATABASE_URL), después el prefijado del .env global
// (LIBRARY_SERVICE_DATABASE_URL) y por último el valor por defecto de local.
func FirstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// Env es FirstNonEmpty leyendo variables de entorno, que es como se usa
// siempre: Env("GRPC_PORT", "USER_SERVICE_GRPC_PORT") con un valor por defecto
// al final si hace falta.
func Env(names ...string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

// InternalSecret es el secreto compartido que autentica las llamadas entre
// servicios. Se lee igual en los tres, así que vive aquí.
//
// Es obligatorio y sin valor por defecto a propósito: un default convertiría
// "se me olvidó configurarlo" en un sistema que parece protegido y no lo está.
// Vale más que el servicio no arranque.
func InternalSecret() (string, error) {
	secret := Env("INTERNAL_SERVICE_TOKEN")
	if secret == "" {
		return "", fmt.Errorf("INTERNAL_SERVICE_TOKEN is required: it authenticates service-to-service calls")
	}
	return secret, nil
}
