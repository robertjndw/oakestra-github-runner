// Command ghrunner is the GitHub runner addon for Oakestra: it serves the dashboard UI bundle
// and its API, and runs the controller that turns queued GitHub Actions jobs into single-use
// runner services on Oakestra.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/robertjndw/oakestra-github-runner/internal/api"
	"github.com/robertjndw/oakestra-github-runner/internal/config"
	"github.com/robertjndw/oakestra-github-runner/internal/controller"
	"github.com/robertjndw/oakestra-github-runner/internal/core"
	"github.com/robertjndw/oakestra-github-runner/internal/github"
	"github.com/robertjndw/oakestra-github-runner/internal/oakestra"
	"github.com/robertjndw/oakestra-github-runner/internal/store"
	"github.com/robertjndw/oakestra-github-runner/internal/webui"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ghrunner:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	key, err := store.LoadOrCreateKey(cfg.DataDir, cfg.EncryptionKey)
	if err != nil {
		return fmt.Errorf("load encryption key: %w", err)
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "ghrunner.db"), key)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	oak := oakestra.New(oakestra.Config{
		BaseURL:     cfg.OakestraAPIURL,
		Credentials: credentialSource(cfg, st),
		Logger:      logger,
	})

	scaleSets := github.NewScaleSetClientFactory(version, logger)
	ctrl := controller.New(controller.Config{
		Store:        st,
		Oakestra:     oak,
		Clients:      controller.NewScaleSetClientFactory(scaleSets.NewClient),
		DefaultImage: cfg.RunnerImage,
		Logger:       logger,
	})

	deps := api.Deps{
		Store:          st,
		Oakestra:       oak,
		GitHub:         github.NewApps(nil),
		Controller:     ctrl,
		EnvCredentials: cfg.HasEnvCredentials(),
		Version:        version,
		RunnerImage:    cfg.RunnerImage,
		UI:             webui.FS(),
		Logger:         logger,
	}
	if cfg.Auth == config.AuthJWT {
		verifier, err := api.NewJWTVerifier(api.JWTOptions{
			PublicKeyPEM: cfg.JWTPublicKey,
			PublicKeyURL: cfg.JWTPublicKeyURL,
			GeneratorURL: cfg.JWTGeneratorURL,
			Logger:       logger,
		})
		if err != nil {
			return fmt.Errorf("set up token verification: %w", err)
		}
		verifier.Start(ctx)
		deps.Verifier = verifier
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.New(deps),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctrlDone := make(chan error, 1)
	go func() { ctrlDone <- ctrl.Run(ctx) }()
	srvErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Listen, "version", version)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			srvErr <- fmt.Errorf("http server: %w", err)
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-srvErr:
		stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && runErr == nil {
		runErr = err
	}

	// Wait for the controller before the deferred store close. It deletes its message sessions
	// on GitHub while shutting down; a session left behind makes GitHub refuse the next start's
	// session with a conflict until the old one expires, and no pool gets jobs in that window.
	select {
	case err := <-ctrlDone:
		if err != nil && runErr == nil {
			runErr = err
		}
	case <-time.After(30 * time.Second):
		logger.Warn("controller did not shut down in time")
	}
	return runErr
}

// credentialSource prefers the environment over what an admin stored in the UI. Env values are
// baked into the addon definition, so when they are set the operator chose them deliberately
// and the UI shows them as read-only.
func credentialSource(cfg config.Config, st core.Store) func(context.Context) (core.OakestraCredentials, string, error) {
	return func(ctx context.Context) (core.OakestraCredentials, string, error) {
		if cfg.HasEnvCredentials() {
			return core.OakestraCredentials{
				Username:     cfg.OakestraUsername,
				Password:     cfg.OakestraPassword,
				Organization: cfg.OakestraOrganization,
			}, "env", nil
		}
		creds, err := st.OakestraCredentials(ctx)
		if errors.Is(err, core.ErrNotFound) {
			return core.OakestraCredentials{}, "", core.ErrNoCredentials
		}
		return creds, "stored", err
	}
}
