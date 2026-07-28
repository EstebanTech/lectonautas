package service

import (
	"net/mail"
	"regexp"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// textField liga el nombre de un campo con los limites de longitud que lo
// gobiernan. Antes los tres viajaban sueltos como argumentos de cada llamada
// (`boundedText("bio", req.GetBio(), bioMaxLen)`), asi que nada impedia pasar el
// nombre de un campo con el tope de otro: eran dos datos sin relacion que habia
// que acordarse de mantener en sintonia, y el error resultante habria mentido.
// Aqui se declaran juntos una sola vez y el call site ya no puede desparejarlos.
type textField struct {
	name string
	// minLen solo gobierna a los campos obligatorios. En los opcionales la
	// cadena vacia siempre vale, porque es como se dice "sin valor".
	minLen int
	maxLen int
}

// Los campos de texto del perfil, con el tope que declara la migracion.
var (
	displayNameField = textField{name: "display_name", maxLen: 100}
	avatarURLField   = textField{name: "avatar_url", maxLen: 500}
	bioField         = textField{name: "bio", maxLen: 1000}

	usernameField = textField{name: "username", minLen: 3, maxLen: 30}
	// El tope de la password es el de bcrypt: mas alla de 72 bytes la funcion
	// ignora el resto en silencio, y una password que en realidad se trunca es
	// peor que una rechazada.
	passwordField = textField{name: "password", minLen: 8, maxLen: 72}
)

// El username es el handle publico, asi que se mantiene restringido: solo
// minusculas, digitos, guion y guion bajo.
var usernamePattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

// Required valida un campo obligatorio y lo deja sin espacios alrededor.
func (f textField) Required(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", status.Errorf(codes.InvalidArgument, "%s is required", f.name)
	}
	if len(v) < f.minLen || len(v) > f.maxLen {
		return "", status.Errorf(codes.InvalidArgument,
			"%s must be between %d and %d characters", f.name, f.minLen, f.maxLen)
	}
	return v, nil
}

// Bounded recorta y valida la longitud maxima de un campo opcional; la cadena
// vacia es valida y significa "sin valor".
func (f textField) Bounded(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if len(v) > f.maxLen {
		return "", status.Errorf(codes.InvalidArgument,
			"%s must be at most %d characters", f.name, f.maxLen)
	}
	return v, nil
}

// Optional es Bounded devolviendo nil cuando el campo viene vacio, para escribir
// NULL en vez de una cadena vacia.
func (f textField) Optional(raw string) (*string, error) {
	v, err := f.Bounded(raw)
	if err != nil {
		return nil, err
	}
	if v == "" {
		return nil, nil
	}
	return &v, nil
}

// Update valida el campo de un update parcial. Absorbe el `if req.X != nil` que
// antes se repetia campo por campo en el handler: nil entra y nil sale, que es
// "no lo toques". La cadena vacia si es significativa aqui (limpia la columna),
// asi que a diferencia de Optional no se convierte a nil.
func (f textField) Update(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	v, err := f.Bounded(*raw)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func normalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" {
		return "", status.Error(codes.InvalidArgument, "email is required")
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return "", status.Error(codes.InvalidArgument, "email is not a valid address")
	}
	return email, nil
}

// normalizeUsername baja a minusculas ANTES de validar, no despues: el alfabeto
// que exige usernamePattern es el de la forma ya normalizada, que es la que se
// guarda y contra la que se comparan los duplicados.
func normalizeUsername(raw string) (string, error) {
	username, err := usernameField.Required(strings.ToLower(raw))
	if err != nil {
		return "", err
	}
	if !usernamePattern.MatchString(username) {
		return "", status.Error(codes.InvalidArgument,
			"username may only contain lowercase letters, digits, hyphens and underscores")
	}
	return username, nil
}

// validatePassword no recorta ni normaliza, a diferencia del resto: los espacios
// de una password son parte de la password, y limpiarlos en silencio cambiaria
// la credencial que el usuario cree haber elegido.
func validatePassword(raw string) error {
	if raw == "" {
		return status.Errorf(codes.InvalidArgument, "%s is required", passwordField.name)
	}
	if len(raw) < passwordField.minLen || len(raw) > passwordField.maxLen {
		return status.Errorf(codes.InvalidArgument, "%s must be between %d and %d characters",
			passwordField.name, passwordField.minLen, passwordField.maxLen)
	}
	return nil
}
