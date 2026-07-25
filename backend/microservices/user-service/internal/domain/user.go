package domain

import "time"

type User struct {
	ID          string
	Email       string
	Username    string
	Password    string
	DisplayName *string
	AvatarURL   *string
	Bio         *string
	IsActive    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// UserUpdate describe una modificacion parcial: los campos en nil se dejan
// intactos, los demas se escriben (una cadena vacia limpia la columna).
type UserUpdate struct {
	ID          string
	Username    *string
	DisplayName *string
	AvatarURL   *string
	Bio         *string
	IsActive    *bool
}
