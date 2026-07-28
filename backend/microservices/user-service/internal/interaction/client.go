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
	"google.golang.org/grpc/credentials/insecure"

	interactionv1 "github.com/EstebanTech/lectonautas/backend/microservices/user-service/proto/interaction/v1"
)

type Client struct {
	conn   *grpc.ClientConn
	client interactionv1.InteractionServiceClient
}

func New(addr string) (*Client, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
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
