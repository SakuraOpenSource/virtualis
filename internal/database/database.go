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

// Migrate runs AutoMigrate for all models and applies data migrations.
func Migrate(db *gorm.DB) error {
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// SC-03 data migration: blank out historical plaintext agent tokens.
	// Agents re-present their token on every heartbeat, which repopulates
	// the in-memory RPC cache; nothing keeps a usable node credential in
	// the database or its backups. The column may not exist on fresh
	// installs, so the update is best-effort there.
	if db.Migrator().HasColumn(&model.Agent{}, "token") {
		if err := db.Model(&model.Agent{}).Where("token <> ''").Update("token", "").Error; err != nil {
			return fmt.Errorf("clear agent plaintext tokens: %w", err)
		}
	}
	// REV-MIGRATION-LEGACY-VPC data migration: retained (non-terminal)
	// migrations recorded before the source_vpc_id column exists have NULL
	// there, yet their suspended source runtime still needs the source
	// network for manual recovery. Backfill the reference from the
	// instance's own vpc_id while it still points at the source; rows that
	// cannot be resolved stay NULL and DeleteVPC treats them as
	// unresolvable-but-pinning (see service.DeleteVPC) instead of
	// reference-free.
	if err := backfillLegacyMigrationSourceVPC(db); err != nil {
		return fmt.Errorf("backfill legacy migration source vpc: %w", err)
	}
	return nil
}

// backfillLegacyMigrationSourceVPC fills source_vpc_id for retained
// migrations whose row is NULL, using the instance's current vpc_id. After
// the historical DB switch the instance no longer points at the source, so
// only rows where the instance is still on the SAME agent as the
// migration's source can be resolved unambiguously.
func backfillLegacyMigrationSourceVPC(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.Migration{}) || !db.Migrator().HasColumn(&model.Migration{}, "source_vpc_id") {
		return nil
	}
	return db.Exec(`
		UPDATE migrations
		SET source_vpc_id = (
			SELECT i.vpc_id FROM instances i
			WHERE i.id = migrations.instance_id
			  AND i.agent_id = migrations.source_agent_id
			  AND i.vpc_id IS NOT NULL
		)
		WHERE source_vpc_id IS NULL
		  AND stage NOT IN ('completed', 'failed')
	`).Error
}
