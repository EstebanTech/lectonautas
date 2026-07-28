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
	"google.golang.org/grpc/credentials/insecure"

	interactionv1 "github.com/EstebanTech/lectonautas/backend/microservices/library-service/proto/interaction/v1"
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

func (c *Client) DeleteBookInteractions(ctx context.Context, bookID string) error {
	_, err := c.client.DeleteBookInteractions(ctx, &interactionv1.DeleteBookInteractionsRequest{
		BookId: bookID,
	})
	return err
}
