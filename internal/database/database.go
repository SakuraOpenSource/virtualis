package database

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/SakuraOpenSource/virtualis/internal/config"
	"github.com/SakuraOpenSource/virtualis/internal/model"
)

// Open creates a gorm connection according to cfg.
func Open(cfg config.Database) (*gorm.DB, error) {
	dsn, err := cfg.DSN()
	if err != nil {
		return nil, err
	}

	var dial gorm.Dialector
	switch cfg.Driver {
	case config.DriverSQLite:
		dir := filepath.Dir(cfg.Path)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("create db dir: %w", err)
			}
		}
		dial = sqlite.Open(dsn)
	case config.DriverMySQL:
		dial = mysql.Open(dsn)
	case config.DriverPostgres:
		dial = postgres.Open(dsn)
	default:
		return nil, fmt.Errorf("unsupported database driver %s", cfg.Driver)
	}

	db, err := gorm.Open(dial, &gorm.Config{
		Logger:                                   logger.Default.LogMode(logger.Warn),
		DisableForeignKeyConstraintWhenMigrating: true,
		TranslateError:                           true,
	})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql db: %w", err)
	}
	if cfg.Driver == config.DriverSQLite {
		sqlDB.SetMaxOpenConns(1)
	} else {
		sqlDB.SetMaxOpenConns(25)
		sqlDB.SetMaxIdleConns(5)
		sqlDB.SetConnMaxLifetime(time.Hour)
	}
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return db, nil
}

// TestConnection opens a temporary connection and closes it.
func TestConnection(cfg config.Database) error {
	db, err := Open(cfg)
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Migrate runs AutoMigrate for all models.
func Migrate(db *gorm.DB) error {
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}
