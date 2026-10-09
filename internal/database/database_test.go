package database

import (
	"path/filepath"
	"testing"

	"github.com/SakuraOpenSource/virtualis/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "db.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { raw, _ := db.DB(); _ = raw.Close() })
	return db
}

// SC-03 data migration: existing rows that still carry a plaintext token in
// a legacy `token` column must be blanked when Migrate runs, because agent
// credentials must not survive in the database or its backups.
func TestMigrateBlanksLegacyPlaintextAgentTokens(t *testing.T) {
	db := openTestDB(t)
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatal(err)
	}
	// Recreate the legacy plaintext column the way an older schema had it.
	if err := db.Exec("ALTER TABLE agents ADD COLUMN token text").Error; err != nil {
		t.Fatalf("add legacy column: %v", err)
	}
	agent := model.Agent{Name: "legacy", TokenHash: "hash-only"}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE agents SET token = ? WHERE id = ?", "super-secret-plaintext", agent.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.Raw("SELECT token FROM agents WHERE id = ?", agent.ID).Scan(&raw).Error; err != nil {
		t.Fatal(err)
	}
	if raw != "" {
		t.Fatalf("legacy plaintext token not blanked: %q", raw)
	}
}
