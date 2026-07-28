package service

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/events"
	interactionv1 "github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/proto/interaction/v1"
)

// Estas pruebas cubren lo que hace distinto a este servicio: la idempotencia de
// me gusta, quien puede tocar cada comentario, el rango de la calificacion y
// que toda escritura avise por el bus. Son las reglas que mas caro sale romper
// en silencio.

func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("se esperaba error con codigo %v, no hubo error", want)
	}
	if got := status.Code(err); got != want {
		t.Fatalf("codigo = %v, se esperaba %v (error: %v)", got, want, err)
	}
}

// --- Me gusta -------------------------------------------------------------------

func TestLikeBook_EsIdempotente(t *testing.T) {
	ts := newTestService(asReader())
	ctx := context.Background()
	req := &interactionv1.LikeBookRequest{BookId: bookID}

	primero, err := ts.svc.LikeBook(ctx, req)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if primero.GetLikes().GetCount() != 1 {
		t.Fatalf("conteo = %d, se esperaba 1", primero.GetLikes().GetCount())
	}

	// El mismo lector otra vez: es el doble clic, no un error ni un voto mas.
	segundo, err := ts.svc.LikeBook(ctx, req)
	if err != nil {
		t.Fatalf("dar me gusta dos veces no deberia fallar: %v", err)
	}
	if segundo.GetLikes().GetCount() != 1 {
		t.Fatalf("conteo = %d tras repetir, se esperaba 1", segundo.GetLikes().GetCount())
	}
	if !segundo.GetLikes().GetLikedByMe() {
		t.Fatal("likedByMe deberia ser true para quien acaba de darlo")
	}
}

func TestUnlikeBook_EsIdempotente(t *testing.T) {
	ts := newTestService(asReader())
	ctx := context.Background()

	// Quitar lo que nunca se puso deja el mismo estado y no es un error.
	resp, err := ts.svc.UnlikeBook(ctx, &interactionv1.UnlikeBookRequest{BookId: bookID})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resp.GetLikes().GetCount() != 0 || resp.GetLikes().GetLikedByMe() {
		t.Fatalf("resumen = %+v, se esperaba vacio", resp.GetLikes())
	}
}

func TestLikes_CuentanUnaVezPorLector(t *testing.T) {
	ts := newTestService(asReader())
	ctx := context.Background()

	if _, err := ts.svc.LikeBook(ctx, &interactionv1.LikeBookRequest{BookId: bookID}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	// Otro lector sobre el mismo libro: ahora si son dos.
	ts.svc.auth = asIntruder()
	resp, err := ts.svc.LikeBook(ctx, &interactionv1.LikeBookRequest{BookId: bookID})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resp.GetLikes().GetCount() != 2 {
		t.Fatalf("conteo = %d, se esperaban 2", resp.GetLikes().GetCount())
	}

	// Y para un tercero que no dio ninguno, likedByMe es false aunque haya dos.
	ts.svc.auth = asAuthor()
	visto, err := ts.svc.GetBookLikes(ctx, &interactionv1.GetBookLikesRequest{BookId: bookID})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if visto.GetLikes().GetCount() != 2 || visto.GetLikes().GetLikedByMe() {
		t.Fatalf("resumen = %+v; se esperaban 2 me gusta y likedByMe false", visto.GetLikes())
	}
}

func TestGetBookLikes_EsPublico(t *testing.T) {
	ts := newTestService(anonymous())

	resp, err := ts.svc.GetBookLikes(context.Background(), &interactionv1.GetBookLikesRequest{BookId: bookID})
	if err != nil {
		t.Fatalf("un anonimo deberia poder ver el conteo: %v", err)
	}
	if resp.GetLikes().GetLikedByMe() {
		t.Fatal("sin token, likedByMe tiene que ser false")
	}
}

// --- Libro y sesion ---------------------------------------------------------------

func TestEscrituras_ExigenTokenYLibroPublicado(t *testing.T) {
	casos := []struct {
		nombre string
		correr func(*InteractionService) error
	}{
		{"like", func(s *InteractionService) error {
			_, err := s.LikeBook(context.Background(), &interactionv1.LikeBookRequest{BookId: bookID})
			return err
		}},
		{"comentario", func(s *InteractionService) error {
			_, err := s.CreateComment(context.Background(), &interactionv1.CreateCommentRequest{
				BookId: bookID, Body: "hola",
			})
			return err
		}},
		{"calificacion", func(s *InteractionService) error {
			_, err := s.RateBook(context.Background(), &interactionv1.RateBookRequest{BookId: bookID, Score: 4})
			return err
		}},
	}

	for _, tt := range casos {
		t.Run(tt.nombre+"/sin token", func(t *testing.T) {
			ts := newTestService(anonymous())
			requireCode(t, tt.correr(ts.svc), codes.Unauthenticated)
		})

		t.Run(tt.nombre+"/token invalido", func(t *testing.T) {
			ts := newTestService(fakeAuth{invalid: true})
			requireCode(t, tt.correr(ts.svc), codes.Unauthenticated)
		})
	}
}

// Un borrador (o un libro que no existe) responde NotFound: library-service no
// lo resuelve sin token, y aqui no se inventa una regla propia.
func TestEscrituras_SobreLibroNoPublicado(t *testing.T) {
	ts := newTestService(asReader())

	_, err := ts.svc.CreateComment(context.Background(), &interactionv1.CreateCommentRequest{
		BookId: draftID, Body: "hola",
	})

	requireCode(t, err, codes.NotFound)
}

// El token se comprueba ANTES que el libro: si fuera al reves, este endpoint le
// diria a cualquiera sin credenciales que un id de libro existe o no.
func TestEscrituras_ElTokenSeComprubaAntesQueElLibro(t *testing.T) {
	ts := newTestService(anonymous())

	_, err := ts.svc.LikeBook(context.Background(), &interactionv1.LikeBookRequest{BookId: missingID})

	requireCode(t, err, codes.Unauthenticated)
	if ts.books.calls != 0 {
		t.Fatal("no se debe preguntar por el libro antes de saber quien pregunta")
	}
}

// Si library-service no responde, no es que el libro no exista: es que ahora no
// se puede saber. Decir NotFound haria que el cliente borrara de su pantalla un
// libro que sigue estando.
func TestEscrituras_ConLibraryServiceCaido(t *testing.T) {
	ts := newTestService(asReader())
	ts.books.down = true

	_, err := ts.svc.LikeBook(context.Background(), &interactionv1.LikeBookRequest{BookId: bookID})

	requireCode(t, err, codes.Unavailable)
}

// Las lecturas publicas son el camino caliente: no deben gastar una llamada al
// vecino en cada visita.
func TestListComments_NoConsultaALibraryService(t *testing.T) {
	ts := newTestService(anonymous())

	if _, err := ts.svc.ListComments(context.Background(), &interactionv1.ListCommentsRequest{
		BookId: bookID,
	}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if ts.books.calls != 0 {
		t.Fatalf("se consulto %d veces a library-service en una lectura", ts.books.calls)
	}
}

// --- Comentarios ------------------------------------------------------------------

func TestCreateComment_ValidaElCuerpo(t *testing.T) {
	casos := []struct {
		nombre string
		body   string
	}{
		{"vacio", ""},
		{"solo espacios", "      "},
		{"demasiado largo", string(make([]byte, bodyMaxLen+1))},
	}

	for _, tt := range casos {
		t.Run(tt.nombre, func(t *testing.T) {
			ts := newTestService(asReader())
			_, err := ts.svc.CreateComment(context.Background(), &interactionv1.CreateCommentRequest{
				BookId: bookID, Body: tt.body,
			})
			requireCode(t, err, codes.InvalidArgument)
		})
	}
}

func TestUpdateComment_SoloSuAutor(t *testing.T) {
	t.Run("un tercero no puede", func(t *testing.T) {
		ts := newTestService(asReader())
		c := ts.comentarioDe(readerID, "mio")

		ts.svc.auth = asIntruder()
		_, err := ts.svc.UpdateComment(context.Background(), &interactionv1.UpdateCommentRequest{
			BookId: bookID, Id: c.ID, Body: "editado por otro",
		})

		requireCode(t, err, codes.PermissionDenied)
	})

	// Ni siquiera el autor del libro: moderar es poder borrar lo que sobra, no
	// poner palabras en boca de otro.
	t.Run("el autor del libro tampoco", func(t *testing.T) {
		ts := newTestService(asReader())
		c := ts.comentarioDe(readerID, "mio")

		ts.svc.auth = asAuthor()
		_, err := ts.svc.UpdateComment(context.Background(), &interactionv1.UpdateCommentRequest{
			BookId: bookID, Id: c.ID, Body: "editado por el autor del libro",
		})

		requireCode(t, err, codes.PermissionDenied)
	})

	t.Run("su autor si, y queda marcado como editado", func(t *testing.T) {
		ts := newTestService(asReader())
		c := ts.comentarioDe(readerID, "mio")

		resp, err := ts.svc.UpdateComment(context.Background(), &interactionv1.UpdateCommentRequest{
			BookId: bookID, Id: c.ID, Body: "ya lo pense mejor",
		})
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if resp.GetComment().GetBody() != "ya lo pense mejor" {
			t.Fatalf("cuerpo = %q", resp.GetComment().GetBody())
		}
		if !resp.GetComment().GetEdited() {
			t.Fatal("un comentario editado tiene que venir marcado como tal")
		}
	})
}

func TestDeleteComment_SuAutorOElDelLibro(t *testing.T) {
	t.Run("su autor", func(t *testing.T) {
		ts := newTestService(asReader())
		c := ts.comentarioDe(readerID, "mio")

		if _, err := ts.svc.DeleteComment(context.Background(), &interactionv1.DeleteCommentRequest{
			BookId: bookID, Id: c.ID,
		}); err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
	})

	// La moderacion: la obra es de alguien, y ese alguien puede sacar lo que no
	// quiere debajo.
	t.Run("el autor del libro modera", func(t *testing.T) {
		ts := newTestService(asReader())
		c := ts.comentarioDe(readerID, "spam")

		ts.svc.auth = asAuthor()
		if _, err := ts.svc.DeleteComment(context.Background(), &interactionv1.DeleteCommentRequest{
			BookId: bookID, Id: c.ID,
		}); err != nil {
			t.Fatalf("el autor del libro deberia poder moderar: %v", err)
		}
	})

	t.Run("un tercero no", func(t *testing.T) {
		ts := newTestService(asReader())
		c := ts.comentarioDe(readerID, "mio")

		ts.svc.auth = asIntruder()
		_, err := ts.svc.DeleteComment(context.Background(), &interactionv1.DeleteCommentRequest{
			BookId: bookID, Id: c.ID,
		})

		requireCode(t, err, codes.PermissionDenied)
	})
}

// El id del libro es parte de la ruta: pedir un comentario por el libro
// equivocado es un 404, no el comentario.
func TestComentario_DeOtroLibroEs404(t *testing.T) {
	ts := newTestService(asReader())
	c := ts.comentarioDe(readerID, "mio")

	_, err := ts.svc.UpdateComment(context.Background(), &interactionv1.UpdateCommentRequest{
		BookId: draftID, Id: c.ID, Body: "editado",
	})

	requireCode(t, err, codes.NotFound)
}

func TestListComments_PaginaYTotal(t *testing.T) {
	ts := newTestService(asReader())
	for i := 0; i < 25; i++ {
		ts.comentarioDe(readerID, "comentario")
	}

	resp, err := ts.svc.ListComments(context.Background(), &interactionv1.ListCommentsRequest{
		BookId: bookID, Page: 1, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(resp.GetComments()) != 10 {
		t.Fatalf("pagina = %d comentarios, se esperaban 10", len(resp.GetComments()))
	}
	// El total es el del libro, no el de la pagina.
	if resp.GetTotal() != 25 {
		t.Fatalf("total = %d, se esperaban 25", resp.GetTotal())
	}
}

// --- Calificaciones ------------------------------------------------------------------

func TestRateBook_ValidaElRango(t *testing.T) {
	for _, score := range []int32{0, -1, 6, 100} {
		ts := newTestService(asReader())
		_, err := ts.svc.RateBook(context.Background(), &interactionv1.RateBookRequest{
			BookId: bookID, Score: score,
		})
		requireCode(t, err, codes.InvalidArgument)
	}

	// 1 y 5 son los extremos validos, no valores prohibidos.
	for _, score := range []int32{1, 5} {
		ts := newTestService(asReader())
		if _, err := ts.svc.RateBook(context.Background(), &interactionv1.RateBookRequest{
			BookId: bookID, Score: score,
		}); err != nil {
			t.Fatalf("la nota %d deberia valer: %v", score, err)
		}
	}
}

// Un lector tiene una sola nota por libro: volver a calificar la reemplaza, no
// suma otro voto.
func TestRateBook_ReemplazaElVotoPropio(t *testing.T) {
	ts := newTestService(asReader())
	ctx := context.Background()

	if _, err := ts.svc.RateBook(ctx, &interactionv1.RateBookRequest{BookId: bookID, Score: 2}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	resp, err := ts.svc.RateBook(ctx, &interactionv1.RateBookRequest{BookId: bookID, Score: 5})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if resp.GetRating().GetCount() != 1 {
		t.Fatalf("votos = %d, se esperaba 1: recalificar no agrega otro", resp.GetRating().GetCount())
	}
	if resp.GetRating().GetAverage() != 5 {
		t.Fatalf("promedio = %v, se esperaba 5", resp.GetRating().GetAverage())
	}
	if resp.GetRating().GetMyScore() != 5 {
		t.Fatalf("myScore = %d, se esperaba 5", resp.GetRating().GetMyScore())
	}
}

func TestRating_PromedioDeVariosLectores(t *testing.T) {
	ts := newTestService(asReader())
	ctx := context.Background()

	ts.svc.RateBook(ctx, &interactionv1.RateBookRequest{BookId: bookID, Score: 5})
	ts.svc.auth = asIntruder()
	ts.svc.RateBook(ctx, &interactionv1.RateBookRequest{BookId: bookID, Score: 2})

	ts.svc.auth = anonymous()
	resp, err := ts.svc.GetBookRating(ctx, &interactionv1.GetBookRatingRequest{BookId: bookID})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resp.GetRating().GetCount() != 2 || resp.GetRating().GetAverage() != 3.5 {
		t.Fatalf("resumen = %+v, se esperaban 2 votos y promedio 3.5", resp.GetRating())
	}
	// Sin token no hay voto propio que mostrar.
	if resp.GetRating().GetMyScore() != 0 {
		t.Fatalf("myScore = %d sin token, se esperaba 0", resp.GetRating().GetMyScore())
	}
}

// Sin votos el promedio es 0, y es count quien lo distingue de una nota baja.
func TestRating_SinVotos(t *testing.T) {
	ts := newTestService(anonymous())

	resp, err := ts.svc.GetBookRating(context.Background(), &interactionv1.GetBookRatingRequest{BookId: bookID})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resp.GetRating().GetCount() != 0 || resp.GetRating().GetAverage() != 0 {
		t.Fatalf("resumen = %+v, se esperaba vacio", resp.GetRating())
	}
}

func TestDeleteRating_SinHaberVotado(t *testing.T) {
	ts := newTestService(asReader())

	_, err := ts.svc.DeleteRating(context.Background(), &interactionv1.DeleteRatingRequest{BookId: bookID})

	requireCode(t, err, codes.NotFound)
}

// --- Resumen --------------------------------------------------------------------

func TestGetBookInteractions_JuntaLosTres(t *testing.T) {
	ts := newTestService(asReader())
	ctx := context.Background()

	ts.svc.LikeBook(ctx, &interactionv1.LikeBookRequest{BookId: bookID})
	ts.svc.RateBook(ctx, &interactionv1.RateBookRequest{BookId: bookID, Score: 4})
	ts.comentarioDe(readerID, "que buen libro")

	resp, err := ts.svc.GetBookInteractions(ctx, &interactionv1.GetBookInteractionsRequest{BookId: bookID})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resp.GetLikes().GetCount() != 1 {
		t.Fatalf("me gusta = %d, se esperaba 1", resp.GetLikes().GetCount())
	}
	if resp.GetRating().GetAverage() != 4 || resp.GetRating().GetCount() != 1 {
		t.Fatalf("calificacion = %+v, se esperaba promedio 4 con 1 voto", resp.GetRating())
	}
	if resp.GetCommentCount() != 1 {
		t.Fatalf("comentarios = %d, se esperaba 1", resp.GetCommentCount())
	}
}

// --- Eventos y cache -----------------------------------------------------------------

// Cada escritura tiene que avisar por el bus: sin eso el WebSocket no tiene
// nada que contar y la mitad del servicio no sirve. Y tiene que invalidar el
// cache, o los GET seguirian dando el contador viejo.
func TestEscrituras_PublicanEventoEInvalidanCache(t *testing.T) {
	casos := []struct {
		nombre string
		correr func(*testService)
		tipo   string
	}{
		{"like", func(ts *testService) {
			ts.svc.LikeBook(context.Background(), &interactionv1.LikeBookRequest{BookId: bookID})
		}, events.TypeLikeChanged},
		{"unlike", func(ts *testService) {
			ts.svc.UnlikeBook(context.Background(), &interactionv1.UnlikeBookRequest{BookId: bookID})
		}, events.TypeLikeChanged},
		{"comentario", func(ts *testService) {
			ts.svc.CreateComment(context.Background(), &interactionv1.CreateCommentRequest{
				BookId: bookID, Body: "hola",
			})
		}, events.TypeCommentCreated},
		{"calificacion", func(ts *testService) {
			ts.svc.RateBook(context.Background(), &interactionv1.RateBookRequest{BookId: bookID, Score: 3})
		}, events.TypeRatingChanged},
	}

	for _, tt := range casos {
		t.Run(tt.nombre, func(t *testing.T) {
			ts := newTestService(asReader())
			tt.correr(ts)

			if len(ts.bus.published) != 1 {
				t.Fatalf("eventos publicados = %d, se esperaba 1", len(ts.bus.published))
			}
			evt := ts.bus.last()
			if evt.Type != tt.tipo {
				t.Fatalf("tipo = %q, se esperaba %q", evt.Type, tt.tipo)
			}
			if evt.BookID != bookID {
				t.Fatalf("el evento tiene que decir de que libro habla, trajo %q", evt.BookID)
			}
			if ts.cache.bumps != 1 {
				t.Fatalf("invalidaciones = %d, se esperaba 1", ts.cache.bumps)
			}
		})
	}
}

// El evento de borrado no lleva el comentario (ya no existe) pero si su id, que
// es lo que el cliente necesita para quitarlo de la lista.
func TestDeleteComment_PublicaElIdBorrado(t *testing.T) {
	ts := newTestService(asReader())
	c := ts.comentarioDe(readerID, "para borrar")

	if _, err := ts.svc.DeleteComment(context.Background(), &interactionv1.DeleteCommentRequest{
		BookId: bookID, Id: c.ID,
	}); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	evt := ts.bus.last()
	if evt.Type != events.TypeCommentDeleted {
		t.Fatalf("tipo = %q", evt.Type)
	}
	if evt.CommentID != c.ID {
		t.Fatalf("commentId = %q, se esperaba %q", evt.CommentID, c.ID)
	}
	if evt.CommentCount == nil || *evt.CommentCount != 0 {
		t.Fatal("el evento tiene que traer el conteo nuevo para mover el contador")
	}
}

// Una lectura no cambia nada, asi que no puede invalidar el cache de todos.
func TestLecturas_NoInvalidanElCache(t *testing.T) {
	ts := newTestService(anonymous())
	ctx := context.Background()

	ts.svc.GetBookLikes(ctx, &interactionv1.GetBookLikesRequest{BookId: bookID})
	ts.svc.GetBookRating(ctx, &interactionv1.GetBookRatingRequest{BookId: bookID})
	ts.svc.ListComments(ctx, &interactionv1.ListCommentsRequest{BookId: bookID})
	ts.svc.GetBookInteractions(ctx, &interactionv1.GetBookInteractionsRequest{BookId: bookID})

	if ts.cache.bumps != 0 {
		t.Fatalf("una lectura invalido el cache %d veces", ts.cache.bumps)
	}
	if len(ts.bus.published) != 0 {
		t.Fatalf("una lectura publico %d eventos", len(ts.bus.published))
	}
}

// --- Limpieza -------------------------------------------------------------------

func TestDeleteUserInteractions_BorraSoloLoSuyo(t *testing.T) {
	ts := newTestService(asReader())
	ctx := context.Background()

	ts.svc.LikeBook(ctx, &interactionv1.LikeBookRequest{BookId: bookID})
	ts.svc.RateBook(ctx, &interactionv1.RateBookRequest{BookId: bookID, Score: 4})
	ts.comentarioDe(readerID, "mio")
	// De otro lector, que no se puede llevar por delante.
	ts.comentarioDe(intruderID, "ajeno")

	resp, err := ts.svc.DeleteUserInteractions(ctx, &interactionv1.DeleteUserInteractionsRequest{
		UserId: readerID,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resp.GetLikesDeleted() != 1 || resp.GetCommentsDeleted() != 1 || resp.GetRatingsDeleted() != 1 {
		t.Fatalf("borrados = %+v, se esperaba 1 de cada", resp)
	}

	n, _ := ts.comments.CountByBook(ctx, bookID)
	if n != 1 {
		t.Fatalf("quedaron %d comentarios, se esperaba solo el del otro lector", n)
	}
}

// Idempotente: sobre alguien que ya no tiene nada devuelve ceros. Es lo que
// permite que user-service reintente si su propio borrado fallo despues.
func TestDeleteUserInteractions_EsIdempotente(t *testing.T) {
	ts := newTestService(asReader())

	resp, err := ts.svc.DeleteUserInteractions(context.Background(),
		&interactionv1.DeleteUserInteractionsRequest{UserId: readerID})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resp.GetLikesDeleted() != 0 || resp.GetCommentsDeleted() != 0 || resp.GetRatingsDeleted() != 0 {
		t.Fatalf("borrados = %+v, se esperaban ceros", resp)
	}
}

func TestDeleteBookInteractions_BorraTodoLoDelLibro(t *testing.T) {
	ts := newTestService(asReader())
	ctx := context.Background()

	ts.svc.LikeBook(ctx, &interactionv1.LikeBookRequest{BookId: bookID})
	ts.comentarioDe(readerID, "uno")
	ts.comentarioDe(intruderID, "dos")

	resp, err := ts.svc.DeleteBookInteractions(ctx, &interactionv1.DeleteBookInteractionsRequest{
		BookId: bookID,
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if resp.GetCommentsDeleted() != 2 || resp.GetLikesDeleted() != 1 {
		t.Fatalf("borrados = %+v, se esperaban 2 comentarios y 1 me gusta", resp)
	}
}
