package service

import (
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"
)

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{3,32}$`)
var instanceNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{1,62}$`)

const MinPasswordLength = 8

// ValidateUsername validates username format.
func ValidateUsername(name string) error {
	if !usernamePattern.MatchString(name) {
		return BadRequest("username must be 3-32 chars: letters, digits, _ or -")
	}
	return nil
}

// ValidateEmail validates and normalizes email.
func ValidateEmail(email string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	addr, err := mail.ParseAddress(normalized)
	if err != nil || addr.Address != normalized {
		return "", BadRequest("invalid email format")
	}
	if utf8.RuneCountInString(normalized) > 255 {
		return "", BadRequest("email too long")
	}
	return normalized, nil
}

// ValidatePassword checks password strength.
func ValidatePassword(pw string) error {
	if utf8.RuneCountInString(pw) < MinPasswordLength {
		return BadRequest("password must be at least %d characters", MinPasswordLength)
	}
	if len(pw) > 72 {
		return BadRequest("password must not exceed 72 bytes")
	}
	return nil
}

// ValidateInstanceName validates instance name.
func ValidateInstanceName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return BadRequest("instance name required")
	}
	if utf8.RuneCountInString(name) < 2 || utf8.RuneCountInString(name) > 64 {
		return BadRequest("instance name must be 2-64 characters")
	}
	if !instanceNamePattern.MatchString(name) {
		return BadRequest("instance name must start with alphanumeric and contain only letters, digits, _ or -")
	}
	return nil
}
