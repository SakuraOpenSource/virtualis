package service

import (
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/SakuraOpenSource/virtualis/internal/auth"
	"github.com/SakuraOpenSource/virtualis/internal/model"
)

// UserService handles single-admin account operations.
// There is no Register: only the initial admin created during Install.
type UserService struct {
	db *gorm.DB
}

// NewUserService creates a UserService.
func NewUserService(db *gorm.DB) *UserService {
	return &UserService{db: db}
}

// Login authenticates by username or email.
func (s *UserService) Login(identifier, password string) (*model.User, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" || password == "" {
		return nil, BadRequest("username and password required")
	}
	var user model.User
	err := s.db.First(&user, "username = ? OR email = ?", identifier, strings.ToLower(identifier)).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, Unauthorized("invalid credentials")
		}
		return nil, err
	}
	if !auth.CheckPassword(user.PasswordHash, password) {
		return nil, Unauthorized("invalid credentials")
	}
	if user.Status != model.StatusActive {
		return nil, Forbidden("account disabled")
	}
	return &user, nil
}

// Get returns user by id.
func (s *UserService) Get(userID uint) (*model.User, error) {
	return s.requireUser(userID)
}

// ChangeEmail updates email after verifying current password.
func (s *UserService) ChangeEmail(userID uint, password, newEmail string) error {
	user, err := s.requireUser(userID)
	if err != nil {
		return err
	}
	if !auth.CheckPassword(user.PasswordHash, password) {
		return Forbidden("incorrect password")
	}
	email, err := ValidateEmail(newEmail)
	if err != nil {
		return err
	}
	if email == user.Email {
		return BadRequest("new email is the same as current")
	}
	var count int64
	if err := s.db.Model(&model.User{}).Where("email = ? AND id <> ?", email, userID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return Conflict("email already in use")
	}
	return s.db.Model(&model.User{}).Where("id = ?", userID).Update("email", email).Error
}

// ChangePassword updates password after verifying old password.
func (s *UserService) ChangePassword(userID uint, oldPassword, newPassword string) error {
	user, err := s.requireUser(userID)
	if err != nil {
		return err
	}
	if !auth.CheckPassword(user.PasswordHash, oldPassword) {
		return Forbidden("incorrect password")
	}
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}
	if auth.CheckPassword(user.PasswordHash, newPassword) {
		return BadRequest("new password must differ from old")
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.db.Model(&model.User{}).Where("id = ?", userID).Update("password_hash", hash).Error
}

func (s *UserService) requireUser(userID uint) (*model.User, error) {
	var user model.User
	if err := s.db.First(&user, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NotFound("user not found")
		}
		return nil, err
	}
	return &user, nil
}
