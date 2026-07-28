package service

import (
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/domain"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// textField liga el nombre de un campo con su limite de longitud. Antes los dos
// viajaban sueltos como argumentos de cada llamada
// (`requiredText("title", req.GetTitle(), titleMaxLen)`), asi que nada impedia
// pasar el nombre de un campo con el tope de otro: eran dos datos sin relacion
// que habia que acordarse de mantener en sintonia, repetidos en cada uno de los
// dieciseis sitios que validan texto. Aqui se declaran juntos una sola vez.
type textField struct {
	name   string
	maxLen int
}

// Los campos de texto del servicio, con el tope que declara su columna en las
// migraciones.
var (
	titleField       = textField{name: "title", maxLen: 255}
	descriptionField = textField{name: "description", maxLen: 5000}
	coverURLField    = textField{name: "cover_url", maxLen: 500}
	contentField     = textField{name: "content", maxLen: 500000}
)

// Required valida un campo obligatorio y lo deja sin espacios alrededor.
func (f textField) Required(value string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", status.Errorf(codes.InvalidArgument, "%s is required", f.name)
	}
	if len(v) > f.maxLen {
		return "", status.Errorf(codes.InvalidArgument,
			"%s must be at most %d characters", f.name, f.maxLen)
	}
	return v, nil
}

// Optional devuelve nil para el valor vacio, que es como se representa "sin
// dato" en las columnas nullables.
func (f textField) Optional(value string) (*string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return nil, nil
	}
	if len(v) > f.maxLen {
		return nil, status.Errorf(codes.InvalidArgument,
			"%s must be at most %d characters", f.name, f.maxLen)
	}
	return &v, nil
}

// Update valida el campo de un update parcial: nil entra y nil sale, que es "no
// lo toques". Aqui la cadena vacia si es significativa (limpia la columna), asi
// que a diferencia de Optional no se convierte a nil.
func (f textField) Update(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	v := strings.TrimSpace(*value)
	if len(v) > f.maxLen {
		return nil, status.Errorf(codes.InvalidArgument,
			"%s must be at most %d characters", f.name, f.maxLen)
	}
	return &v, nil
}

// UpdateRequired es Update para los campos que no admiten quedar vacios (el
// titulo es NOT NULL).
func (f textField) UpdateRequired(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	v, err := f.Required(*value)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func validateBookStatus(value string) (string, error) {
	switch value {
	case domain.BookStatusDraft, domain.BookStatusPublished, domain.BookStatusArchived:
		return value, nil
	default:
		return "", status.Errorf(codes.InvalidArgument, "status must be one of: %s, %s, %s",
			domain.BookStatusDraft, domain.BookStatusPublished, domain.BookStatusArchived)
	}
}

func validateChapterStatus(value string) (string, error) {
	switch value {
	case domain.ChapterStatusDraft, domain.ChapterStatusPublished:
		return value, nil
	default:
		return "", status.Errorf(codes.InvalidArgument, "status must be one of: %s, %s",
			domain.ChapterStatusDraft, domain.ChapterStatusPublished)
	}
}

// validateGenres normaliza y valida la lista de generos de un libro. Que los
// slugs existan de verdad no se comprueba aqui: eso lo dice la FK contra el
// catalogo, en la misma escritura, sin una consulta de mas ni una ventana entre
// el chequeo y el INSERT.
func validateGenres(values []string) ([]string, error) {
	if len(values) > domain.GenreMaxPerBook {
		return nil, status.Errorf(codes.InvalidArgument,
			"a book can have at most %d genres", domain.GenreMaxPerBook)
	}

	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, v := range values {
		slug := strings.ToLower(strings.TrimSpace(v))
		if slug == "" {
			return nil, status.Error(codes.InvalidArgument, "genres must not contain empty values")
		}
		// Repetir un genero no es lo mismo que pedir cuatro: se rechaza en vez
		// de deduplicar en silencio, que dejaria pasar una lista que el cliente
		// cree de cuatro y en realidad es de dos.
		if seen[slug] {
			return nil, status.Errorf(codes.InvalidArgument, "genre %q is repeated", slug)
		}
		seen[slug] = true
		out = append(out, slug)
	}
	return out, nil
}

// optionalGenreFilter valida el genero que se usa como filtro de un listado. No
// se comprueba contra el catalogo: un slug que no existe simplemente no tiene
// libros, igual que un author_id sin obra.
func optionalGenreFilter(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// requiredID valida que un identificador venga presente. Que sea o no un UUID
// real lo resuelve el repositorio, que traduce el error del driver a NotFound:
// para una busqueda puntual, un id con forma invalida y uno que no existe son
// lo mismo.
func requiredID(field, value string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", status.Errorf(codes.InvalidArgument, "%s is required", field)
	}
	return v, nil
}

// requiredIDs valida la lista completa que reciben los dos reordenamientos, que
// esperan todos los elementos exactamente una vez. Un id repetido dejaria
// elementos sin posicion asignada y el conteo cuadraria igual, asi que se
// descarta aqui y no en el repositorio.
func requiredIDs(field string, ids []string) error {
	if len(ids) == 0 {
		return status.Errorf(codes.InvalidArgument, "%s is required", field)
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			return status.Errorf(codes.InvalidArgument, "%s cannot contain empty values", field)
		}
		if _, dup := seen[id]; dup {
			return status.Errorf(codes.InvalidArgument, "%s cannot contain duplicates", field)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// optionalUUIDFilter valida un id que se usa como FILTRO de un listado, donde
// la regla anterior no sirve: Postgres rechaza el UUID malformado con un error
// que el repositorio traduce a NotFound, y un listado respondiendo
// "book not found" no tiene sentido. Se valida antes de llegar a la consulta.
func optionalUUIDFilter(field, value string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", nil
	}
	if !isUUID(v) {
		return "", status.Errorf(codes.InvalidArgument, "%s must be a valid UUID", field)
	}
	return v, nil
}

// isUUID comprueba la forma 8-4-4-4-12 en hexadecimal. Alcanza para no mandarle
// basura a Postgres, que es lo unico que se busca aqui; no valida la version
// del UUID ni hace falta.
func isUUID(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i, c := range v {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

// normalizePage acota la pagina pedida a lo que el servicio esta dispuesto a
// servir. Es una sola funcion sobre los dos campos y no una por tipo de filtro:
// antes habia una copia para libros y otra identica para sagas, y la regla es la
// misma.
func normalizePage(page, pageSize int32) (int32, int32) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return page, pageSize
}

// requiredPosition valida la posicion que se le pide a un capitulo o a un libro
// dentro de su saga.
func requiredPosition(value int32) error {
	if value < 0 {
		return status.Error(codes.InvalidArgument, "position must be greater than zero")
	}
	return nil
}
