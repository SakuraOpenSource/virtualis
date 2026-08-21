package storage

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Errors.
var (
	ErrTooLarge = errors.New("file too large")
	ErrEmpty    = errors.New("file is empty")
	ErrBadPath  = errors.New("invalid file path")
)

const (
	dirPerm  = 0o700
	filePerm = 0o600
	sniffLen = 512
)

// Store persists uploaded files under dataDir/images.
type Store struct {
	root string
}

// New creates a Store rooted at <dataDir>/images.
func New(dataDir string) *Store {
	return &Store{root: filepath.Join(dataDir, "images")}
}

// Root returns storage root.
func (s *Store) Root() string { return s.root }

// Save stores r under category and returns relative path, size and mime.
func (s *Store) Save(category string, r io.Reader, limit int64) (string, int64, string, error) {
	if err := validateSegment(category); err != nil {
		return "", 0, "", err
	}
	dir := filepath.Join(category)
	name, err := randomHex(16)
	if err != nil {
		return "", 0, "", err
	}
	rel := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Join(s.root, dir), dirPerm); err != nil {
		return "", 0, "", fmt.Errorf("mkdir: %w", err)
	}
	abs := filepath.Join(s.root, rel)
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return "", 0, "", fmt.Errorf("create file: %w", err)
	}
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(abs)
	}
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()

	head := make([]byte, sniffLen)
	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", 0, "", fmt.Errorf("read: %w", err)
	}
	head = head[:n]
	if n == 0 {
		return "", 0, "", ErrEmpty
	}
	mime := http.DetectContentType(head)

	body := io.MultiReader(bytes.NewReader(head), r)
	written, err := io.Copy(f, io.LimitReader(body, limit+1))
	if err != nil {
		return "", 0, "", fmt.Errorf("write: %w", err)
	}
	if written > limit {
		return "", 0, "", ErrTooLarge
	}
	if err := f.Close(); err != nil {
		return "", 0, "", fmt.Errorf("close: %w", err)
	}
	cleanup = nil
	return filepath.ToSlash(rel), written, mime, nil
}

// Open opens a previously saved file.
func (s *Store) Open(rel string) (*os.File, error) {
	abs, err := s.resolve(rel)
	if err != nil {
		return nil, err
	}
	return os.Open(abs)
}

// Remove deletes a file; missing file is ok.
func (s *Store) Remove(rel string) error {
	abs, err := s.resolve(rel)
	if err != nil {
		return err
	}
	if err := os.Remove(abs); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// RemoveAll deletes multiple files.
func (s *Store) RemoveAll(paths []string) error {
	var first error
	for _, p := range paths {
		if p == "" {
			continue
		}
		if err := s.Remove(p); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (s *Store) resolve(rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.ContainsRune(rel, '\x00') {
		return "", ErrBadPath
	}
	clean := filepath.Clean(filepath.FromSlash(strings.ReplaceAll(rel, `\`, "/")))
	if clean == "." || filepath.IsAbs(clean) {
		return "", ErrBadPath
	}
	abs := filepath.Join(s.root, clean)
	got, err := filepath.Rel(s.root, abs)
	if err != nil || got == ".." || strings.HasPrefix(got, ".."+string(filepath.Separator)) {
		return "", ErrBadPath
	}
	return abs, nil
}

func validateSegment(name string) error {
	if name == "" || len(name) > 32 {
		return ErrBadPath
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return ErrBadPath
	}
	return nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}
