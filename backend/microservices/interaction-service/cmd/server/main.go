package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/auth"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/config"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/content"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/events"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/repository"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/server"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/service"
	"github.com/EstebanTech/lectonautas/backend/shared/cache"
	sharedconfig "github.com/EstebanTech/lectonautas/backend/shared/config"
	"github.com/EstebanTech/lectonautas/backend/shared/database"
	"github.com/EstebanTech/lectonautas/backend/shared/logx"
)

// cachePrefix es el espacio de nombres de este servicio en Valkey, que esta
// compartido con los demas y con el rate limiting.
const cachePrefix = "inter:"

func main() {
	logx.Setup("interaction-service")

	if path, err := sharedconfig.LoadRootDotEnv(); err != nil {
		slog.Info("no global .env loaded, using environment variables", slog.String("error", err.Error()))
	} else {
		slog.Info("loaded environment", slog.String("path", path))
	}

	cfg, err := config.Load()
	if err != nil {
		fatal("failed to load config", err)
	}

	pool, err := database.NewPostgresPool(cfg.DatabaseURL)
	if err != nil {
		fatal("failed to connect to database", err)
	}
	defer pool.Close()

	valkey := cache.NewClient(cfg.RedisAddr)
	defer valkey.Close()

	pingCtx, cancelPing := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelPing()
	if err := valkey.Ping(pingCtx); err != nil {
		fatal("failed to connect to valkey", err, slog.String("addr", cfg.RedisAddr))
	}

	// Las conexiones a los vecinos son perezosas: no conectan hasta la primera
	// llamada, asi que arrancar antes que ellos no rompe nada.
	authenticator, err := auth.New(cfg.UserServiceAddr, cfg.InternalSecret)
	if err != nil {
		fatal("failed to create user-service client", err, slog.String("addr", cfg.UserServiceAddr))
	}
	defer authenticator.Close()

	books, err := content.New(cfg.LibraryServiceAddr, cfg.InternalSecret)
	if err != nil {
		fatal("failed to create library-service client", err, slog.String("addr", cfg.LibraryServiceAddr))
	}
	defer books.Close()

	interactionCache := cache.NewVersioned(valkey, cachePrefix)
	bus := events.NewBus(valkey.Redis())

	interactionService := service.NewInteractionService(
		repository.NewPostgresLikeRepository(pool),
		repository.NewPostgresCommentRepository(pool),
		repository.NewPostgresRatingRepository(pool),
		repository.NewPostgresCleanupRepository(pool),
		books,
		interactionCache,
		authenticator,
		bus,
	)

	// El hub se alimenta del bus, no de las escrituras locales: asi da igual que
	// el evento lo haya producido esta instancia u otra.
	busCtx, cancelBus := context.WithCancel(context.Background())
	defer cancelBus()

	hub := server.NewHub()
	go hub.Run(bus.Subscribe(busCtx))

	// Dos servidores en el mismo proceso porque son dos protocolos que no se
	// pueden multiplexar en un listener: gRPC (que el gateway transcodifica a
	// REST) y HTTP/1.1 para el WebSocket, que el transcoder no sabe manejar y
	// llega crudo.
	grpcServer := server.NewGRPCServer(interactionService, cfg.InternalSecret)
	lis, err := net.Listen("tcp", ":"+cfg.GRPCPort)
	if err != nil {
		fatal("failed to listen", err, slog.String("port", cfg.GRPCPort))
	}

	wsHandler := server.NewWSHandler(hub, interactionService, books, authenticator)
	httpServer := &http.Server{
		Addr:    ":" + cfg.WSPort,
		Handler: wsHandler.Routes(),
		// Sin WriteTimeout: mataria las conexiones WebSocket, que estan abiertas
		// por definicion. El plazo por escritura lo pone el propio handler, que
		// es donde se puede distinguir un envio colgado de una conexion sana en
		// silencio.
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("gRPC server listening", slog.String("port", cfg.GRPCPort))
		if err := grpcServer.Serve(lis); err != nil {
			fatal("failed to serve gRPC", err)
		}
	}()

	go func() {
		slog.Info("WebSocket server listening", slog.String("port", cfg.WSPort))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal("failed to serve HTTP", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	slog.Info("shutting down gracefully")

	// El HTTP se cierra con plazo: los WebSocket abiertos no terminan solos, y
	// sin el limite el apagado se quedaria esperando a que el ultimo lector
	// cierre la pestana.
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("http shutdown", slog.String("error", err.Error()))
	}

	grpcServer.GracefulStop()
}

// fatal deja el error en el log estructurado y termina. Sustituye a
// log.Fatalf, que escribia en un formato distinto al del resto y se saltaba el
// handler de slog.
func fatal(msg string, err error, attrs ...slog.Attr) {
	args := make([]any, 0, len(attrs)+1)
	args = append(args, slog.String("error", err.Error()))
	for _, a := range attrs {
		args = append(args, a)
	}
	slog.Error(msg, args...)
	os.Exit(1)
}
