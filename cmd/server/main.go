package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"happylearn.local/app/internal/app"
	"happylearn.local/app/internal/platform/config"
)

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		log.Print("configuration_error")
		os.Exit(1)
	}

	handler := app.New(app.Dependencies{Ready: func(context.Context) error { return nil }})
	server := newServer(cfg.ListenAddress, handler)

	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Print("server_start_error")
			os.Exit(1)
		}
	case <-signals.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Print("server_shutdown_error")
			os.Exit(1)
		}
	}
}

func newServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}
