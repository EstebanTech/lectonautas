// Package interaction habla con interaction-service para borrar lo que un
// usuario deja atras al darse de baja: sus me gusta, sus comentarios y sus
// calificaciones.
//
// Es el mismo caso que el paquete content con library-service: viven en otra
// base de datos y solo su servicio dueno puede borrarlos, asi que no hay CASCADE
// que los alcance. La conexion es perezosa, de modo que la dependencia no afecta
// al arranque.
package interaction

import (
	"context"

	"google.golang.org/grpc"

	interactionv1 "github.com/EstebanTech/lectonautas/backend/microservices/user-service/proto/interaction/v1"
	"github.com/EstebanTech/lectonautas/backend/shared/grpcx"
)

type Client struct {
	conn   *grpc.ClientConn
	client interactionv1.InteractionServiceClient
}

func New(addr, internalSecret string) (*Client, error) {
	conn, err := grpcx.Dial(addr, internalSecret)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, client: interactionv1.NewInteractionServiceClient(conn)}, nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}

// DeleteUserInteractions borra todo lo que el usuario dejo como lector. El error
// se propaga tal cual: quien llama decide, y lo que hace es no borrar la cuenta.
func (c *Client) DeleteUserInteractions(ctx context.Context, userID string) error {
	_, err := c.client.DeleteUserInteractions(ctx, &interactionv1.DeleteUserInteractionsRequest{
		UserId: userID,
	})
	return err
}
