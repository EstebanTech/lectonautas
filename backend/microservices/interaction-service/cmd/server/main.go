package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/auth"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/cache"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/config"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/content"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/database"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/events"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/repository"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/server"
	"github.com/EstebanTech/lectonautas/backend/microservices/interaction-service/internal/service"
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

	pingCtx, cancelPing := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelPing()
	if err := valkey.Ping(pingCtx); err != nil {
		log.Fatalf("failed to connect to valkey (%s): %v", cfg.RedisAddr, err)
	}

	// Las conexiones a los vecinos son perezosas: no conectan hasta la primera
	// llamada, asi que arrancar antes que ellos no rompe nada.
	authenticator, err := auth.New(cfg.UserServiceAddr)
	if err != nil {
		log.Fatalf("failed to create user-service client (%s): %v", cfg.UserServiceAddr, err)
	}
	defer authenticator.Close()

	books, err := content.New(cfg.LibraryServiceAddr)
	if err != nil {
		log.Fatalf("failed to create library-service client (%s): %v", cfg.LibraryServiceAddr, err)
	}
	defer books.Close()

	interactionCache := cache.NewInteractionCache(valkey)
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
	grpcServer := server.NewGRPCServer(interactionService)
	lis, err := net.Listen("tcp", ":"+cfg.GRPCPort)
	if err != nil {
		log.Fatalf("failed to listen on port %s: %v", cfg.GRPCPort, err)
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
		log.Printf("interaction-service gRPC server listening on :%s", cfg.GRPCPort)
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("failed to serve gRPC: %v", err)
		}
	}()

	go func() {
		log.Printf("interaction-service WebSocket server listening on :%s", cfg.WSPort)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("failed to serve HTTP: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("shutting down gracefully...")

	// El HTTP se cierra con plazo: los WebSocket abiertos no terminan solos, y
	// sin el limite el apagado se quedaria esperando a que el ultimo lector
	// cierre la pestana.
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown: %v", err)
	}

	grpcServer.GracefulStop()
}
