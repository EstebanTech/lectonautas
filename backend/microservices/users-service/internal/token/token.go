// Package token genera y hashea los tokens de sesion. El token en crudo se
// entrega al cliente una sola vez (en el login) y nunca se persiste; lo que se
// guarda en la BD y en Valkey es su hash.
package token

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// New genera un token de sesion aleatorio y opaco (32 bytes, ~256 bits de
// entropia) codificado en base64 url-safe.
func New() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Hash devuelve el SHA-256 (hex) del token. A diferencia de bcrypt es
// determinista, lo que permite buscar la sesion por su hash tanto en Valkey
// como en la BD. Es seguro para tokens porque son valores aleatorios de alta
// entropia (no contrasenas que se puedan adivinar por fuerza bruta).
func Hash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
