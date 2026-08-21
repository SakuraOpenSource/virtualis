package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	DriverSQLite   = "sqlite"
	DriverMySQL    = "mysql"
	DriverPostgres = "postgres"
)

type Database struct {
	Driver   string `json:"driver"`
	Path     string `json:"path,omitempty"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
	Name     string `json:"name,omitempty"`
}

type Config struct {
	Database  Database `json:"database"`
	JWTSecret string   `json:"jwt_secret"`
	Listen    string   `json:"listen"`
}

const DefaultListen = ":8080"

func Path(dataDir string) string { return filepath.Join(dataDir, "config.json") }

func Exists(dataDir string) bool {
	_, err := os.Stat(Path(dataDir))
	return err == nil
}

func Load(dataDir string) (*Config, error) {
	raw, err := os.ReadFile(Path(dataDir))
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	return &c, nil
}

func Save(dataDir string, c *Config) error {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(c, "", "  ")
	tmp, err := os.CreateTemp(dataDir, "config-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	_ = tmp.Chmod(0600)
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	return os.Rename(name, Path(dataDir))
}

func GenerateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (d Database) Validate() error {
	switch d.Driver {
	case DriverSQLite:
		if d.Path == "" {
			return fmt.Errorf("sqlite path required")
		}
	case DriverMySQL, DriverPostgres:
		if d.Host == "" || d.Port == 0 || d.User == "" || d.Name == "" {
			return fmt.Errorf("database host/port/user/name required")
		}
	default:
		return fmt.Errorf("unsupported driver %s", d.Driver)
	}
	return nil
}

func (d Database) DSN() (string, error) {
	if err := d.Validate(); err != nil {
		return "", err
	}
	switch d.Driver {
	case DriverSQLite:
		return d.Path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", nil
	case DriverMySQL:
		return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=UTC", d.User, d.Password, d.Host, d.Port, d.Name), nil
	case DriverPostgres:
		return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", d.User, d.Password, d.Host, d.Port, d.Name), nil
	}
	return "", fmt.Errorf("unsupported driver")
}
