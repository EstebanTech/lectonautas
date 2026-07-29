package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/cache"
	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/config"
	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/content"
	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/interaction"
	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/repository"
	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/server"
	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/service"
	sharedcache "github.com/EstebanTech/lectonautas/backend/shared/cache"
	sharedconfig "github.com/EstebanTech/lectonautas/backend/shared/config"
	"github.com/EstebanTech/lectonautas/backend/shared/database"
	"github.com/EstebanTech/lectonautas/backend/shared/logx"
)

// cachePrefix es el espacio de nombres de este servicio en Valkey, que esta
// compartido con los demas y con el rate limiting.
const cachePrefix = "user:"

func main() {
	logx.Setup("user-service")

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

	valkey := sharedcache.NewClient(cfg.RedisAddr)
	defer valkey.Close()

	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := valkey.Ping(pingCtx); err != nil {
		fatal("failed to connect to valkey", err, slog.String("addr", cfg.RedisAddr))
	}

	// Dos caches con reglas distintas sobre el mismo Valkey: el de sesiones
	// borra por clave (revocacion) y el de usuarios invalida por version
	// (frescura), como en los otros dos servicios.
	sessionCache := cache.NewSessionCache(valkey)
	userCache := sharedcache.NewVersioned(valkey, cachePrefix)

	// La conexion es perezosa, asi que no importa que library-service todavia
	// no este arriba: solo se usa al dar de baja una cuenta.
	contentClient, err := content.New(cfg.LibraryAddr, cfg.InternalSecret)
	if err != nil {
		fatal("failed to create library-service client", err, slog.String("addr", cfg.LibraryAddr))
	}
	defer contentClient.Close()

	// Igual de perezosa, y por el mismo motivo: solo se usa al dar de baja.
	interactionClient, err := interaction.New(cfg.InteractionAddr, cfg.InternalSecret)
	if err != nil {
		fatal("failed to create interaction-service client", err, slog.String("addr", cfg.InteractionAddr))
	}
	defer interactionClient.Close()

	userRepo := repository.NewPostgresUserRepository(pool)
	sessionRepo := repository.NewPostgresSessionRepository(pool)
	userService := service.NewUserService(userRepo, sessionRepo, sessionCache, userCache, contentClient, interactionClient)
	grpcServer := server.NewGRPCServer(userService, cfg.InternalSecret)

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
