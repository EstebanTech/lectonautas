// Package content habla con library-service para borrar lo que un usuario deja
// atras al darse de baja.
//
// Es la direccion contraria a la habitual (library-service llama aqui para
// validar sesiones), pero no hay alternativa razonable: los libros viven en otra
// base de datos y solo su servicio dueno puede borrarlos. Las dos conexiones son
// perezosas, asi que la dependencia mutua no afecta al arranque.
package content

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	libraryv1 "github.com/EstebanTech/lectonautas/backend/microservices/user-service/proto/library/v1"
)

type Client struct {
	conn   *grpc.ClientConn
	client libraryv1.LibraryServiceClient
}

// New abre la conexion a library-service. grpc.NewClient no conecta al vuelo: la
// conexion se establece en la primera llamada, asi que arrancar antes que
// library-service no es un problema.
func New(addr string) (*Client, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, client: libraryv1.NewLibraryServiceClient(conn)}, nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}

// DeleteAuthorContent borra los libros, sagas y guardados del usuario. El error
// se propaga tal cual: quien llama decide que hacer con la baja si esto falla, y
// lo que hace es no borrar la cuenta.
func (c *Client) DeleteAuthorContent(ctx context.Context, userID string) error {
	_, err := c.client.DeleteAuthorContent(ctx, &libraryv1.DeleteAuthorContentRequest{UserId: userID})
	return err
}
