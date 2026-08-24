package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/SakuraOpenSource/virtualis/internal/auth"
	"github.com/SakuraOpenSource/virtualis/internal/config"
	"github.com/SakuraOpenSource/virtualis/internal/database"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"github.com/SakuraOpenSource/virtualis/internal/runtime"
	"github.com/SakuraOpenSource/virtualis/internal/server"
	"github.com/SakuraOpenSource/virtualis/internal/web"
)

var version = "dev"

func main() {
	var (
		dataDir       = flag.String("data", "data", "directory for config and sqlite file")
		listenAddr    = flag.String("listen", "", "override listen address from config")
		enableDebug   = flag.Bool("debug", false, "enable debug mode")
		showVersion   = flag.Bool("version", false, "print version and exit")
		resetPassword = flag.Bool("reset-password", false, "reset the administrator password and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("virtualis %s\n", version)
		return
	}
	if *resetPassword {
		if err := resetAdminPassword(*dataDir); err != nil {
			log.Fatalf("password reset failed: %v", err)
		}
		return
	}

	if err := run(*dataDir, *listenAddr, *enableDebug); err != nil {
		log.Fatalf("startup failed: %v", err)
	}
}

func resetAdminPassword(dataDir string) error {
	cfg, err := config.Load(dataDir)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	db, err := database.Open(cfg.Database)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	var user model.User
	if err := db.Where("role = ?", model.RoleAdmin).Order("id ASC").First(&user).Error; err != nil {
		return fmt.Errorf("find administrator: %w", err)
	}
	reader := bufio.NewReader(os.Stdin)
	password, err := readPasswordLine(reader, "New administrator password: ")
	if err != nil {
		return err
	}
	confirmation, err := readPasswordLine(reader, "Confirm administrator password: ")
	if err != nil {
		return err
	}
	if password != confirmation {
		return errors.New("passwords do not match")
	}
	if len([]rune(password)) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if err := db.Model(&model.User{}).Where("id = ?", user.ID).Update("password_hash", hash).Error; err != nil {
		return err
	}
	fmt.Printf("administrator password reset for %s\n", user.Username)
	return nil
}

func readPasswordLine(reader *bufio.Reader, prompt string) (string, error) {
	fmt.Print(prompt)
	value, err := reader.ReadString('\n')
	value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	if err != nil && len(value) == 0 {
		return "", err
	}
	return value, nil
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
