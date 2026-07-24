package domain

import "time"

// Session es una sesion activa de un usuario. TokenHash guarda el hash del
// token (SHA-256), nunca el valor en crudo: ese solo lo tiene el cliente.
type Session struct {
	ID         string
	UserID     string
	TokenHash  string
	UserAgent  *string
	LastUsedAt *time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}
