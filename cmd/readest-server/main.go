package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/readest/readest-tiny-backend/internal/config"
	"github.com/readest/readest-tiny-backend/internal/server"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	app, err := server.New(cfg)
	if err != nil {
		slog.Error("initialize server", "error", err)
		os.Exit(1)
	}
	defer app.Close()

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.HTTPIOTimeout,
		WriteTimeout:      cfg.HTTPIOTimeout,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 * 1024,
	}
	serverErrors := make(chan error, 1)
	go func() {
		slog.Info("Readest backend listening", "addr", cfg.Addr, "public_url", cfg.PublicURL)
		serverErrors <- httpServer.ListenAndServe()
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	select {
	case received := <-signals:
		slog.Info("shutdown requested", "signal", received.String())
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server stopped", "error", err)
			os.Exit(1)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
		_ = httpServer.Close()
	}
}
