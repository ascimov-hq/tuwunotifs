package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	configPath := flag.String("config", "/etc/tuwunotifs/config.yaml", "path to config file")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fatal("config", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fcm, projectID, err := NewFCM(context.Background(), cfg.CredentialsFile, cfg.ProjectID)
	if err != nil {
		fatal("fcm", err)
	}

	srv := &http.Server{Addr: cfg.Listen, Handler: NewGateway(fcm, cfg).Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	slog.Info("tuwunotifs started", "listen", cfg.Listen, "project", projectID, "app_ids", cfg.AppIDs)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal("http", err)
	}
	slog.Info("tuwunotifs stopped")
}

func fatal(what string, err error) {
	slog.Error("fatal", "stage", what, "err", err)
	os.Exit(1)
}
