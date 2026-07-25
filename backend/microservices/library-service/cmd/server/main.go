package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/auth"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/cache"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/config"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/database"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/repository"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/server"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/library-service/internal/service"
)

func main() {
	if path, err := config.LoadRootDotEnv(); err != nil {
		log.Printf("no global .env loaded (%v), using environment variables", err)
	} else {
		log.Printf("loaded environment from %s", path)
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	pool, err := database.NewPostgresPool(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer pool.Close()

	valkey := cache.NewClient(cfg.RedisAddr)
	defer valkey.Close()

	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := valkey.Ping(pingCtx); err != nil {
		log.Fatalf("failed to connect to valkey (%s): %v", cfg.RedisAddr, err)
	}

	// La conexion a user-service es perezosa: no conecta hasta la primera
	// validacion de token, asi que arrancar antes que el no rompe nada.
	authenticator, err := auth.New(cfg.UserServiceAddr)
	if err != nil {
		log.Fatalf("failed to create user-service client (%s): %v", cfg.UserServiceAddr, err)
	}
	defer authenticator.Close()

	libraryCache := cache.NewLibraryCache(valkey)

	bookRepo := repository.NewPostgresBookRepository(pool)
	chapterRepo := repository.NewPostgresChapterRepository(pool)
	sagaRepo := repository.NewPostgresSagaRepository(pool)
	savedRepo := repository.NewPostgresSavedBookRepository(pool)

	libraryService := service.NewLibraryService(bookRepo, chapterRepo, sagaRepo, savedRepo, libraryCache, authenticator)
	grpcServer := server.NewGRPCServer(libraryService)

	lis, err := net.Listen("tcp", ":"+cfg.GRPCPort)
	if err != nil {
		log.Fatalf("failed to listen on port %s: %v", cfg.GRPCPort, err)
	}

	go func() {
		log.Printf("library-service gRPC server listening on :%s", cfg.GRPCPort)
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("failed to serve: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("shutting down gracefully...")
	grpcServer.GracefulStop()
}
