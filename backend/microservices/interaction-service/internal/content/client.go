// Package content resuelve contra library-service las preguntas sobre libros
// que este servicio necesita para decidir.
//
// Este servicio no tiene la tabla de libros ni la quiere: no hay foreign key
// que impida comentar un libro inexistente, asi que la comprobacion es una
// llamada gRPC antes de escribir.
package content

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	libraryv1 "github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/proto/library/v1"
)

// Publicado es el unico estado sobre el que se puede interactuar. Un borrador
// todavia no esta a la vista de nadie mas que su autor, y uno archivado se
// retiro: en ninguno de los dos casos tiene sentido dejar un comentario nuevo.
const publicado = "published"

// Book es lo poco que este servicio necesita saber de un libro.
type Book struct {
	ID       string
	AuthorID string
	Status   string
}

type Client struct {
	conn   *grpc.ClientConn
	client libraryv1.LibraryServiceClient
}

// New abre la conexion a library-service. Como el resto de los clientes gRPC
// del monorepo, no conecta al vuelo: la conexion se establece en la primera
// llamada, asi que arrancar antes que library-service no rompe nada.
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

// PublishedBook devuelve el libro solo si esta publicado, y NotFound en
// cualquier otro caso.
//
// La llamada va SIN token a proposito: asi library-service aplica su regla de
// visibilidad publica y un borrador ajeno le responde NotFound directamente. No
// hace falta repetir aqui esa regla, que es suya.
func (c *Client) PublishedBook(ctx context.Context, bookID string) (*Book, error) {
	resp, err := c.client.GetBook(ctx, &libraryv1.GetBookRequest{Id: bookID})
	if err != nil {
		// NotFound e InvalidArgument vienen de que el libro no existe o el id no
		// tiene forma de uuid: para el que pregunta son lo mismo. Cualquier otro
		// codigo (library-service caido, por ejemplo) se propaga como esta, para
		// no disfrazar una caida de "el libro no existe".
		if code := status.Code(err); code == codes.NotFound || code == codes.InvalidArgument {
			return nil, status.Error(codes.NotFound, "book not found")
		}
		return nil, err
	}

	book := resp.GetBook()
	if book == nil || book.GetStatus() != publicado {
		return nil, status.Error(codes.NotFound, "book not found")
	}

	return &Book{ID: book.GetId(), AuthorID: book.GetAuthorId(), Status: book.GetStatus()}, nil
}

// Exists es PublishedBook cuando solo interesa el si o el no. Lo usa el
// WebSocket antes de dejar que alguien se suscriba a un libro.
func (c *Client) Exists(ctx context.Context, bookID string) error {
	_, err := c.PublishedBook(ctx, bookID)
	return err
}
