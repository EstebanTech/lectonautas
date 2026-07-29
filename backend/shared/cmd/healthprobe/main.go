// Command healthprobe pregunta por el health check de gRPC de un servicio y
// devuelve 0 si esta SERVING.
//
// Existe porque las imagenes de los servicios son distroless: no tienen shell
// ni curl, asi que el healthcheck del compose no puede ser un comando de texto
// y necesita un binario. La alternativa era traer grpc-health-probe desde otra
// imagen, que es una dependencia externa mas —y un registro mas del que
// depender en cada build— para treinta lineas que ya se pueden escribir con el
// grpc que este modulo usa igualmente.
//
//	healthprobe -addr :50051 [-timeout 2s]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	addr := flag.String("addr", "", "direccion del servicio (por ejemplo :50051)")
	timeout := flag.Duration("timeout", 2*time.Second, "plazo maximo de la comprobacion")
	flag.Parse()

	if *addr == "" {
		fail("addr is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	conn, err := grpc.NewClient(*addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fail("dial: %v", err)
	}
	defer conn.Close()

	// La cadena vacia pregunta por el servidor entero, que es como lo registra
	// grpcx.RegisterHealth.
	resp, err := grpc_health_v1.NewHealthClient(conn).Check(ctx,
		&grpc_health_v1.HealthCheckRequest{Service: ""})
	if err != nil {
		fail("check: %v", err)
	}

	if resp.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		fail("status: %s", resp.GetStatus())
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
