package repository

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/domain"
)

// Pruebas contra una base real. Cubren lo que los dobles en memoria no pueden:
// el upsert, el ON CONFLICT, el CHECK del rango, las transacciones y la
// traduccion de los errores del driver a errores de dominio.
//
// Se saltan solas si no hay base configurada, para que `go test ./...` siga
// sirviendo sin levantar nada:
//
//	INTERACTION_TEST_DATABASE_URL="postgres://user:password@localhost:5433/interaction_service?sslmode=disable" go test ./internal/repository/
//
// Cada prueba usa ids nuevos y borra lo suyo al terminar, asi que puede correr
// contra la base de desarrollo sin ensuciarla.

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("INTERACTION_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("INTERACTION_TEST_DATABASE_URL no esta definida; se omiten las pruebas de integracion")
	}

	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("no se pudo abrir el pool: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("no se pudo conectar a la base: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

// newID genera un uuid y deja programada la limpieza de todo lo que cuelgue de
// el, sirva de libro o de usuario.
func newID(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()

	var id string
	if err := pool.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatalf("no se pudo generar el id: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		for _, table := range []string{"interaction.likes", "interaction.comments", "interaction.ratings"} {
			pool.Exec(ctx, `DELETE FROM `+table+` WHERE book_id = $1 OR user_id = $1`, id)
		}
	})

	return id
}

// --- Me gusta ---------------------------------------------------------------------

// La idempotencia la sostiene el ON CONFLICT DO NOTHING contra la PK compuesta,
// que es justo lo que un doble en memoria no demuestra.
func TestLikes_RepetirNoDuplica(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	likes := NewPostgresLikeRepository(pool)
	book := newID(t, pool)
	user := newID(t, pool)

	for i := 0; i < 3; i++ {
		s, err := likes.Like(ctx, book, user)
		if err != nil {
			t.Fatalf("dar me gusta la vez %d fallo: %v", i+1, err)
		}
		if s.Count != 1 {
			t.Fatalf("conteo = %d tras %d veces, se esperaba 1", s.Count, i+1)
		}
		if !s.LikedByMe {
			t.Fatal("likedByMe deberia ser true para quien lo dio")
		}
	}
}

// Dos peticiones a la vez del mismo lector es el caso que el ON CONFLICT existe
// para cubrir: sin el, una de las dos chocaria con la PK.
func TestLikes_ALaVezNoChocan(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	likes := NewPostgresLikeRepository(pool)
	book := newID(t, pool)
	user := newID(t, pool)

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = likes.Like(ctx, book, user)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("la peticion %d fallo: %v", i, err)
		}
	}

	s, err := likes.Summary(ctx, book, user)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if s.Count != 1 {
		t.Fatalf("conteo = %d tras 8 peticiones simultaneas, se esperaba 1", s.Count)
	}
}

func TestLikes_QuitarLoQueNoEstabaNoFalla(t *testing.T) {
	pool := testPool(t)
	likes := NewPostgresLikeRepository(pool)
	book := newID(t, pool)
	user := newID(t, pool)

	s, err := likes.Unlike(context.Background(), book, user)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if s.Count != 0 || s.LikedByMe {
		t.Fatalf("resumen = %+v, se esperaba vacio", s)
	}
}

// El anonimo llega con cadena vacia, que no es un uuid: el NULLIF de la
// consulta es lo que evita que Postgres reviente por el cast.
func TestLikes_ResumenParaAnonimo(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	likes := NewPostgresLikeRepository(pool)
	book := newID(t, pool)
	user := newID(t, pool)

	if _, err := likes.Like(ctx, book, user); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	s, err := likes.Summary(ctx, book, "")
	if err != nil {
		t.Fatalf("un anonimo deberia poder leer el conteo: %v", err)
	}
	if s.Count != 1 || s.LikedByMe {
		t.Fatalf("resumen = %+v; se esperaba 1 me gusta y likedByMe false", s)
	}
}

// --- Calificaciones ------------------------------------------------------------------

// El upsert es lo que hace que un lector tenga una sola nota: sin el, volver a
// calificar chocaria con la PK o agregaria otro voto.
func TestRatings_RecalificarReemplaza(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ratings := NewPostgresRatingRepository(pool)
	book := newID(t, pool)
	user := newID(t, pool)

	if _, err := ratings.Rate(ctx, book, user, 2); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	s, err := ratings.Rate(ctx, book, user, 5)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if s.Count != 1 {
		t.Fatalf("votos = %d, se esperaba 1", s.Count)
	}
	if s.Average != 5 || s.MyScore != 5 {
		t.Fatalf("resumen = %+v, se esperaba promedio y voto propio 5", s)
	}
}

func TestRatings_PromedioDeVarios(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ratings := NewPostgresRatingRepository(pool)
	book := newID(t, pool)
	uno := newID(t, pool)
	otro := newID(t, pool)

	ratings.Rate(ctx, book, uno, 5)
	ratings.Rate(ctx, book, otro, 2)

	s, err := ratings.Summary(ctx, book, uno)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if s.Count != 2 || s.Average != 3.5 {
		t.Fatalf("resumen = %+v, se esperaban 2 votos y promedio 3.5", s)
	}
	// El voto propio es el suyo, no el maximo ni el ultimo.
	if s.MyScore != 5 {
		t.Fatalf("myScore = %d, se esperaba 5", s.MyScore)
	}
}

// El rango 1..5 lo impone un CHECK del esquema. El servicio ya valida antes,
// pero esta es la red de abajo.
func TestRatings_ElRangoLoImponeLaBase(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ratings := NewPostgresRatingRepository(pool)
	book := newID(t, pool)
	user := newID(t, pool)

	for _, score := range []int32{0, 6, -1} {
		if _, err := ratings.Rate(ctx, book, user, score); err == nil {
			t.Fatalf("la base acepto la nota %d, que esta fuera de 1..5", score)
		}
	}

	// Los extremos si tienen que entrar.
	for _, score := range []int32{domain.ScoreMin, domain.ScoreMax} {
		if _, err := ratings.Rate(ctx, book, user, score); err != nil {
			t.Fatalf("la base rechazo la nota %d, que es valida: %v", score, err)
		}
	}
}

func TestRatings_SinVotosElPromedioEsCero(t *testing.T) {
	pool := testPool(t)
	ratings := NewPostgresRatingRepository(pool)
	book := newID(t, pool)

	// El COALESCE es lo que evita que avg(NULL) llegue como null y reviente el
	// Scan sobre un float64.
	s, err := ratings.Summary(context.Background(), book, "")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if s.Count != 0 || s.Average != 0 {
		t.Fatalf("resumen = %+v, se esperaba vacio", s)
	}
}

func TestRatings_BorrarSinHaberVotado(t *testing.T) {
	pool := testPool(t)
	ratings := NewPostgresRatingRepository(pool)

	err := ratings.Delete(context.Background(), newID(t, pool), newID(t, pool))

	if !errors.Is(err, ErrRatingNotFound) {
		t.Fatalf("error = %v, se esperaba ErrRatingNotFound", err)
	}
}

// --- Comentarios -----------------------------------------------------------------

func TestComments_CreaListaYCuenta(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	comments := NewPostgresCommentRepository(pool)
	book := newID(t, pool)
	user := newID(t, pool)

	for i := 0; i < 25; i++ {
		if _, err := comments.Create(ctx, &domain.Comment{
			BookID: book, UserID: user, Body: "comentario",
		}); err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
	}

	pagina, total, err := comments.ListByBook(ctx, domain.CommentFilter{
		BookID: book, Page: 1, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(pagina) != 10 || total != 25 {
		t.Fatalf("pagina = %d con total = %d; se esperaban 10 y 25", len(pagina), total)
	}

	n, err := comments.CountByBook(ctx, book)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if n != 25 {
		t.Fatalf("conteo = %d, se esperaban 25", n)
	}
}

// Un comentario recien creado no esta editado; el UPDATE mueve updated_at y es
// eso lo que lo marca.
func TestComments_EditadoSaleDeLasFechas(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	comments := NewPostgresCommentRepository(pool)
	book := newID(t, pool)
	user := newID(t, pool)

	creado, err := comments.Create(ctx, &domain.Comment{BookID: book, UserID: user, Body: "original"})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if creado.Edited() {
		t.Fatal("un comentario recien creado no puede venir marcado como editado")
	}

	actualizado, err := comments.Update(ctx, book, creado.ID, "corregido")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if !actualizado.Edited() {
		t.Fatal("tras editarlo, updated_at deberia ser posterior a created_at")
	}
	if actualizado.Body != "corregido" {
		t.Fatalf("cuerpo = %q", actualizado.Body)
	}
}

// El book_id es parte de la busqueda: un comentario pedido por el libro
// equivocado no aparece.
func TestComments_NoSeAlcanzanDesdeOtroLibro(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	comments := NewPostgresCommentRepository(pool)
	book := newID(t, pool)
	otro := newID(t, pool)
	user := newID(t, pool)

	c, err := comments.Create(ctx, &domain.Comment{BookID: book, UserID: user, Body: "hola"})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if _, err := comments.GetByID(ctx, otro, c.ID); !errors.Is(err, ErrCommentNotFound) {
		t.Fatalf("error = %v, se esperaba ErrCommentNotFound", err)
	}
	if err := comments.Delete(ctx, otro, c.ID); !errors.Is(err, ErrCommentNotFound) {
		t.Fatalf("error = %v, se esperaba ErrCommentNotFound", err)
	}
}

// Un id malformado no es un 500: para una busqueda puntual, malformado e
// inexistente son lo mismo.
func TestComments_IdMalformadoEsNotFound(t *testing.T) {
	pool := testPool(t)
	comments := NewPostgresCommentRepository(pool)

	_, err := comments.GetByID(context.Background(), newID(t, pool), "no-soy-un-uuid")

	if !errors.Is(err, ErrCommentNotFound) {
		t.Fatalf("error = %v, se esperaba ErrCommentNotFound", err)
	}
}

// --- Limpieza ------------------------------------------------------------------

func TestCleanup_PorUsuarioBorraSoloLoSuyo(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cleanup := NewPostgresCleanupRepository(pool)
	likes := NewPostgresLikeRepository(pool)
	comments := NewPostgresCommentRepository(pool)
	ratings := NewPostgresRatingRepository(pool)
	book := newID(t, pool)
	user := newID(t, pool)
	otro := newID(t, pool)

	likes.Like(ctx, book, user)
	ratings.Rate(ctx, book, user, 4)
	comments.Create(ctx, &domain.Comment{BookID: book, UserID: user, Body: "mio"})
	// Del otro lector, que tiene que sobrevivir.
	likes.Like(ctx, book, otro)
	comments.Create(ctx, &domain.Comment{BookID: book, UserID: otro, Body: "ajeno"})

	counts, err := cleanup.DeleteByUser(ctx, user)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if counts.Likes != 1 || counts.Comments != 1 || counts.Ratings != 1 {
		t.Fatalf("borrados = %+v, se esperaba 1 de cada", counts)
	}

	s, _ := likes.Summary(ctx, book, otro)
	if s.Count != 1 || !s.LikedByMe {
		t.Fatalf("el me gusta del otro lector no sobrevivio: %+v", s)
	}
	n, _ := comments.CountByBook(ctx, book)
	if n != 1 {
		t.Fatalf("quedaron %d comentarios, se esperaba solo el ajeno", n)
	}
}

func TestCleanup_PorLibroBorraTodo(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cleanup := NewPostgresCleanupRepository(pool)
	likes := NewPostgresLikeRepository(pool)
	comments := NewPostgresCommentRepository(pool)
	ratings := NewPostgresRatingRepository(pool)
	book := newID(t, pool)
	otroLibro := newID(t, pool)
	user := newID(t, pool)

	likes.Like(ctx, book, user)
	ratings.Rate(ctx, book, user, 3)
	comments.Create(ctx, &domain.Comment{BookID: book, UserID: user, Body: "uno"})
	comments.Create(ctx, &domain.Comment{BookID: book, UserID: user, Body: "dos"})
	// De otro libro, que no se toca.
	comments.Create(ctx, &domain.Comment{BookID: otroLibro, UserID: user, Body: "de otro libro"})

	counts, err := cleanup.DeleteByBook(ctx, book)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if counts.Likes != 1 || counts.Comments != 2 || counts.Ratings != 1 {
		t.Fatalf("borrados = %+v, se esperaban 1, 2 y 1", counts)
	}

	n, _ := comments.CountByBook(ctx, otroLibro)
	if n != 1 {
		t.Fatalf("se borro lo de otro libro: quedan %d", n)
	}
}

// Idempotente: sobre algo que ya no tiene nada devuelve ceros. Es lo que
// permite que el llamante reintente si su propio borrado fallo despues.
func TestCleanup_EsIdempotente(t *testing.T) {
	pool := testPool(t)
	cleanup := NewPostgresCleanupRepository(pool)

	counts, err := cleanup.DeleteByUser(context.Background(), newID(t, pool))
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if counts.Likes != 0 || counts.Comments != 0 || counts.Ratings != 0 {
		t.Fatalf("borrados = %+v, se esperaban ceros", counts)
	}
}
