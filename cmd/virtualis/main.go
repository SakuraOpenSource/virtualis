package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SakuraOpenSource/virtualis/internal/config"
	"github.com/SakuraOpenSource/virtualis/internal/database"
	"github.com/SakuraOpenSource/virtualis/internal/runtime"
	"github.com/SakuraOpenSource/virtualis/internal/server"
	"github.com/SakuraOpenSource/virtualis/internal/web"
)

var version = "dev"

func main() {
	var (
		dataDir     = flag.String("data", "data", "directory for config and sqlite file")
		listenAddr  = flag.String("listen", "", "override listen address from config")
		enableDebug = flag.Bool("debug", false, "enable debug mode")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("virtualis %s\n", version)
		return
	}

	if err := run(*dataDir, *listenAddr, *enableDebug); err != nil {
		log.Fatalf("startup failed: %v", err)
	}
}

func run(dataDir, overrideAddr string, debug bool) error {
	rt := runtime.New(dataDir)
	addr := config.DefaultListen

	if config.Exists(dataDir) {
		cfg, err := config.Load(dataDir)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		db, err := database.Open(cfg.Database)
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		if err := database.Migrate(db); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		rt.Activate(cfg, db)
		addr = cfg.Listen
		log.Printf("config loaded, db driver=%s", cfg.Database.Driver)
	} else {
		log.Printf("no config found, waiting for installation via web")
	}

	if overrideAddr != "" {
		addr = overrideAddr
	}

	if !web.Available() {
		log.Printf("warning: frontend assets not embedded, API only")
	}

	engine, closeFn := server.New(rt, debug)
	defer closeFn()

	srv := &http.Server{
		Addr:              addr,
		Handler:           engine,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("virtualis %s listening on %s (debug=%v)", version, addr, debug)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		} else {
			errCh <- nil
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Printf("shutting down gracefully")
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}
}
