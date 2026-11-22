// Command chatgo is the single-binary entrypoint:
// config → store → services → api → embedded SPA.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"chatgo/internal/api"
	"chatgo/internal/auth"
	"chatgo/internal/chat"
	"chatgo/internal/config"
	"chatgo/internal/files"
	"chatgo/internal/providers"
	"chatgo/internal/search"
	"chatgo/internal/store"
)

// version is overridable via -ldflags "-X main.version=…".
var version = "dev"

func main() {
	flag.Parse()
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if version != "dev" {
		cfg.Version = version
	}

	secret, err := cfg.SecretBytes()
	if err != nil {
		return fmt.Errorf("session secret: %w", err)
	}

	st, err := store.Open(cfg.DatabasePath, secret)
	if err != nil {
		return err
	}
	defer st.Close()

	// Services (scaffold stubs where logic is still owned by later agents).
	prov := providers.NewClient(cfg)
	srch := search.New(cfg)
	filesSvc := files.New(cfg, st)
	chatSvc := chat.New(cfg, st, prov, srch)
	authn, err := auth.New(cfg, st)
	if err != nil {
		return err
	}

	srv := api.New(api.Deps{
		Cfg:       cfg,
		Store:     st,
		Auth:      authn,
		Chat:      chatSvc,
		Search:    srch,
		Files:     filesSvc,
		Providers: prov,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Hourly hygiene: purge expired sessions and stale throttle state;
	// cancelling ctx (signal received) stops the janitor gracefully.
	go authn.RunJanitor(ctx)

	errCh := make(chan error, 1)
	go func() {
		slog.Info("chatgo listening", "addr", cfg.Addr(), "version", cfg.Version)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
