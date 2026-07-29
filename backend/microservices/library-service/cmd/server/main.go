package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/auth"
	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/config"
	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/interaction"
	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/repository"
	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/server"
	"github.com/EstebanTech/lectonautas/backend/microservices/library-service/internal/service"
	"github.com/EstebanTech/lectonautas/backend/shared/cache"
	sharedconfig "github.com/EstebanTech/lectonautas/backend/shared/config"
	"github.com/EstebanTech/lectonautas/backend/shared/database"
	"github.com/EstebanTech/lectonautas/backend/shared/logx"
)

// cachePrefix es el espacio de nombres de este servicio en Valkey, que esta
// compartido con los demas y con el rate limiting.
const cachePrefix = "lib:"

func main() {
	logx.Setup("library-service")

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

	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := valkey.Ping(pingCtx); err != nil {
		fatal("failed to connect to valkey", err, slog.String("addr", cfg.RedisAddr))
	}

	// La conexion a user-service es perezosa: no conecta hasta la primera
	// validacion de token, asi que arrancar antes que el no rompe nada.
	authenticator, err := auth.New(cfg.UserServiceAddr, cfg.InternalSecret)
	if err != nil {
		fatal("failed to create user-service client", err, slog.String("addr", cfg.UserServiceAddr))
	}
	defer authenticator.Close()

	libraryCache := cache.NewVersioned(valkey, cachePrefix)

	bookRepo := repository.NewPostgresBookRepository(pool)
	chapterRepo := repository.NewPostgresChapterRepository(pool)
	sagaRepo := repository.NewPostgresSagaRepository(pool)
	genreRepo := repository.NewPostgresGenreRepository(pool)

	// Al borrar un libro hay que llevarse sus me gusta, comentarios y
	// calificaciones, que viven en la base de interaction-service. Conexion
	// perezosa, como las demas.
	interactionClient, err := interaction.New(cfg.InteractionServiceAddr, cfg.InternalSecret)
	if err != nil {
		fatal("failed to create interaction-service client", err, slog.String("addr", cfg.InteractionServiceAddr))
	}
	defer interactionClient.Close()

	libraryService := service.NewLibraryService(bookRepo, chapterRepo, sagaRepo, genreRepo, libraryCache, authenticator, interactionClient)
	grpcServer := server.NewGRPCServer(libraryService, cfg.InternalSecret)

	lis, err := net.Listen("tcp", ":"+cfg.GRPCPort)
	if err != nil {
		fatal("failed to listen", err, slog.String("port", cfg.GRPCPort))
	}

	go func() {
		slog.Info("gRPC server listening", slog.String("port", cfg.GRPCPort))
		if err := grpcServer.Serve(lis); err != nil {
			fatal("failed to serve", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	slog.Info("shutting down gracefully")
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
