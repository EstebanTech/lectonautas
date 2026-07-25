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
