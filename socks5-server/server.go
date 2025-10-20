package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"dabit.app/socks5/implementation/armon/socks5"
	"dabit.app/socks5/implementation/caarlos0/env"
)

type params struct {
	User     string `env:"PROXY_USER" envDefault:""`
	Password string `env:"PROXY_PASSWORD" envDefault:""`
	Port     string `env:"PROXY_PORT" envDefault:"1080"`
}

func main() {
	// Working with app params
	cfg := params{}
	err := env.Parse(&cfg)
	if err != nil {
		log.Printf("%+v\n", err)
	}

	//Initialize socks5 config
	socsk5conf := &socks5.Config{
		Logger: log.New(os.Stdout, "", log.LstdFlags),
	}

	if cfg.User+cfg.Password != "" {
		creds := socks5.StaticCredentials{
			os.Getenv("PROXY_USER"): os.Getenv("PROXY_PASSWORD"),
		}
		cator := socks5.UserPassAuthenticator{Credentials: creds}
		socsk5conf.AuthMethods = []socks5.Authenticator{cator}
	}

	server, err := socks5.New(socsk5conf)
	if err != nil {
		log.Fatal(err)
	}

	// Create a context that can be cancelled
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start server in a goroutine
	serverErr := make(chan error, 1)
	go func() {
		log.Printf("Start listening proxy service on port %s\n", cfg.Port)
		serverErr <- server.ListenAndServeWithContext(ctx, "tcp", ":"+cfg.Port)
	}()

	// Wait for either server error or shutdown signal
	select {
	case err := <-serverErr:
		if err != nil {
			log.Fatal(err)
		}
	case sig := <-sigChan:
		log.Printf("Received signal %v, shutting down gracefully...\n", sig)
		cancel() // Cancel the context to signal shutdown

		// Wait for server to shutdown gracefully
		if err := server.Shutdown(); err != nil {
			log.Printf("Error during shutdown: %v\n", err)
		} else {
			log.Println("Server shutdown completed")
		}
	}
}
