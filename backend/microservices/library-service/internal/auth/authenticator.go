// Package auth resuelve el token de sesion del llamante contra user-service.
//
// Este servicio NO tiene la tabla de sesiones ni la quiere: la fuente de verdad
// vive en user-service. Se le pregunta por gRPC (rpc ValidateSession) en vez de
// leer directamente la clave que ese servicio cachea en Valkey, porque hacerlo
// obligaria a replicar aqui su forma de hashear el token y, ante un miss de
// cache, no habria a que caer — solo user-service puede repoblarla.
package auth

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	userv1 "github.com/EstebanTech/lectonautas/backend/microservices/library-service/proto/user/v1"
	"github.com/EstebanTech/lectonautas/backend/shared/grpcx"
)

type Authenticator struct {
	conn   *grpc.ClientConn
	client userv1.UserServiceClient
}

// New abre la conexion a user-service. La conexion es perezosa, asi que
// arrancar antes que user-service no es un problema; el secreto viaja en cada
// llamada porque ValidateSession es un metodo interno del vecino.
func New(addr, internalSecret string) (*Authenticator, error) {
	conn, err := grpcx.Dial(addr, internalSecret)
	if err != nil {
		return nil, err
	}
	return &Authenticator{conn: conn, client: userv1.NewUserServiceClient(conn)}, nil
}

func (a *Authenticator) Close() error {
	return a.conn.Close()
}

// Require exige un token valido y devuelve el user_id del llamante. Es lo que
// usan todas las escrituras y los listados de lo propio.
func (a *Authenticator) Require(ctx context.Context) (string, error) {
	raw, err := bearerToken(ctx)
	if err != nil {
		return "", err
	}

	resp, err := a.client.ValidateSession(ctx, &userv1.ValidateSessionRequest{Token: raw})
	if err != nil {
		// Unauthenticated e InvalidArgument vienen de que el token no sirve;
		// se reenvian tal cual. Cualquier otra cosa es un fallo de
		// user-service, y no se le cuenta al cliente como sesion invalida.
		switch status.Code(err) {
		case codes.Unauthenticated, codes.InvalidArgument:
			return "", status.Error(codes.Unauthenticated, "invalid or expired token")
		default:
			return "", status.Error(codes.Internal, "failed to validate session")
		}
	}
	return resp.GetUserId(), nil
}

// Optional resuelve el token si vino, y devuelve cadena vacia si no hay ninguno.
// Lo usan las lecturas publicas: sin token se ve solo lo publicado, y con token
// el autor ve ademas sus propios borradores.
//
// Un token presente pero invalido si es error: callar ese caso haria que un
// token vencido se viera como una respuesta normal recortada, sin pista alguna.
func (a *Authenticator) Optional(ctx context.Context) (string, error) {
	if _, err := bearerToken(ctx); err != nil {
		return "", nil
	}
	return a.Require(ctx)
}

// bearerToken extrae el token en crudo del header "Authorization: Bearer <token>"
// (Envoy lo reenvia como metadata gRPC).
func bearerToken(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing authorization header")
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return "", status.Error(codes.Unauthenticated, "missing authorization header")
	}

	raw := strings.TrimSpace(values[0])
	// Aceptar "Bearer <token>" (cualquier capitalizacion) o el token pelado.
	if i := strings.IndexByte(raw, ' '); i != -1 && strings.EqualFold(raw[:i], "bearer") {
		raw = strings.TrimSpace(raw[i+1:])
	}
	if raw == "" {
		return "", status.Error(codes.Unauthenticated, "missing authorization header")
	}
	return raw, nil
}
