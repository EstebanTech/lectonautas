package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/estebandeveloper20/lectonautas/backend/microservices/user-service/internal/cache"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/user-service/internal/config"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/user-service/internal/database"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/user-service/internal/repository"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/user-service/internal/server"
	"github.com/estebandeveloper20/lectonautas/backend/microservices/user-service/internal/service"
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

	sessionCache := cache.NewSessionCache(valkey)
	userCache := cache.NewUserCache(valkey)

	userRepo := repository.NewPostgresUserRepository(pool)
	sessionRepo := repository.NewPostgresSessionRepository(pool)
	userService := service.NewUserService(userRepo, sessionRepo, sessionCache, userCache)
	grpcServer := server.NewGRPCServer(userService)

	lis, err := net.Listen("tcp", ":"+cfg.GRPCPort)
	if err != nil {
		log.Fatalf("failed to listen on port %s: %v", cfg.GRPCPort, err)
	}

	go func() {
		log.Printf("user-service gRPC server listening on :%s", cfg.GRPCPort)
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
