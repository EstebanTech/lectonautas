// Package interaction habla con interaction-service para borrar lo que colgaba
// de un libro que aqui se borra: sus me gusta, sus comentarios y sus
// calificaciones.
//
// Viven en otra base de datos, asi que el CASCADE que limpia los capitulos y
// los vinculos con sagas no los alcanza. La conexion es perezosa, de modo que
// la dependencia no afecta al arranque.
package interaction

import (
	"context"

	"google.golang.org/grpc"

	interactionv1 "github.com/EstebanTech/lectonautas/backend/microservices/library-service/proto/interaction/v1"
	"github.com/EstebanTech/lectonautas/backend/shared/grpcx"
)

type Client struct {
	conn   *grpc.ClientConn
	client interactionv1.InteractionServiceClient
}

// New abre la conexion a interaction-service. El secreto compartido viaja en
// cada llamada: la limpieza que se le pide es un metodo interno alla.
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

func (c *Client) DeleteBookInteractions(ctx context.Context, bookID string) error {
	_, err := c.client.DeleteBookInteractions(ctx, &interactionv1.DeleteBookInteractionsRequest{
		BookId: bookID,
	})
	return err
}
