package grpcx

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/EstebanTech/lectonautas/backend/shared/logx"
)

// Lo que se afirma aqui es el control de acceso de las llamadas entre
// servicios. Es la pieza mas delicada de este modulo: detras de estos metodos
// hay borrados masivos cuya unica credencial seria un id que ademas es publico,
// asi que un fallo abierto no se notaria hasta que alguien lo usara.

const (
	metodoInterno = "/interaction.v1.InteractionService/DeleteUserInteractions"
	metodoPublico = "/interaction.v1.InteractionService/GetBookInteractions"
	secreto       = "secreto-de-prueba"
)

// handlerEspia responde ok y anota si de verdad llego a correr, que es lo que
// distingue "rechazado" de "aceptado".
type handlerEspia struct {
	llamado bool
	ctx     context.Context
}

func (h *handlerEspia) handle(ctx context.Context, _ any) (any, error) {
	h.llamado = true
	h.ctx = ctx
	return "ok", nil
}

func ctxCon(pares ...string) context.Context {
	if len(pares) == 0 {
		return context.Background()
	}
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(pares...))
}

func TestInternalAuth(t *testing.T) {
	tests := []struct {
		nombre  string
		metodo  string
		secreto string
		ctx     context.Context
		pasa    bool
	}{
		{
			nombre:  "metodo interno sin credencial",
			metodo:  metodoInterno,
			secreto: secreto,
			ctx:     ctxCon(),
			pasa:    false,
		},
		{
			nombre:  "metodo interno con credencial equivocada",
			metodo:  metodoInterno,
			secreto: secreto,
			ctx:     ctxCon(InternalTokenKey, "otra-cosa"),
			pasa:    false,
		},
		{
			nombre:  "metodo interno con la credencial correcta",
			metodo:  metodoInterno,
			secreto: secreto,
			ctx:     ctxCon(InternalTokenKey, secreto),
			pasa:    true,
		},
		{
			// Sin secreto configurado no hay con que comparar. Se falla
			// cerrado: mas vale que la limpieza no funcione a que quede
			// abierta sin que nadie se entere.
			nombre:  "sin secreto configurado no pasa nadie",
			metodo:  metodoInterno,
			secreto: "",
			ctx:     ctxCon(InternalTokenKey, secreto),
			pasa:    false,
		},
		{
			// El resto de la API no lleva credencial de servicio: su control
			// de acceso es el token de sesion, que vive en otra capa.
			nombre:  "metodo publico sin credencial",
			metodo:  metodoPublico,
			secreto: secreto,
			ctx:     ctxCon(),
			pasa:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			espia := &handlerEspia{}
			interceptor := internalAuthInterceptor(tt.secreto, []string{metodoInterno})

			_, err := interceptor(tt.ctx, nil,
				&grpc.UnaryServerInfo{FullMethod: tt.metodo}, espia.handle)

			if tt.pasa {
				if err != nil {
					t.Fatalf("error inesperado: %v", err)
				}
				if !espia.llamado {
					t.Fatal("el handler no llego a correr")
				}
				return
			}

			if espia.llamado {
				t.Fatal("el handler corrio pese a no tener credencial")
			}
			// NotFound y no PermissionDenied: es la misma respuesta que da el
			// gateway para estos metodos, asi que desde fuera no hay forma de
			// saber que existen.
			if status.Code(err) != codes.NotFound {
				t.Fatalf("codigo = %s, se esperaba NotFound", status.Code(err))
			}
		})
	}
}

func TestRequestIDLlegaAlHandler(t *testing.T) {
	const id = "id-de-la-peticion"

	espia := &handlerEspia{}
	_, err := requestIDInterceptor(ctxCon(logx.RequestIDKey, id), nil,
		&grpc.UnaryServerInfo{FullMethod: metodoPublico}, espia.handle)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	// El id que puso Envoy tiene que sobrevivir hasta el handler: es lo que
	// hace que el log de los tres servicios se pueda juntar.
	if got := logx.RequestID(espia.ctx); got != id {
		t.Fatalf("request id = %q, se esperaba %q", got, id)
	}
}

func TestRequestIDSeGeneraSiNoViene(t *testing.T) {
	// Pasa cuando algo llama al servicio directo por gRPC, sin cruzar el
	// gateway: los vecinos entre si, o un grpcurl a mano.
	espia := &handlerEspia{}
	_, err := requestIDInterceptor(ctxCon(), nil,
		&grpc.UnaryServerInfo{FullMethod: metodoPublico}, espia.handle)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if logx.RequestID(espia.ctx) == "" {
		t.Fatal("la peticion llego sin id y no se genero ninguno")
	}
}

// El panic de un handler no puede tumbar el proceso entero: se traduce a un
// error gRPC normal.
func TestRecoveryAtrapaElPanic(t *testing.T) {
	explota := func(context.Context, any) (any, error) { panic("boom") }

	_, err := recoveryInterceptor(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: metodoPublico}, explota)

	if status.Code(err) != codes.Internal {
		t.Fatalf("codigo = %s, se esperaba Internal", status.Code(err))
	}
}
