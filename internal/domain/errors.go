package domain

import (
	"errors"
	"strings"
)

// Sentinel errors shared across packages. The API layer maps them to HTTP status codes.
var (
	ErrNotFound            = errors.New("not found")
	ErrDuplicate           = errors.New("already exists")
	ErrQuotaExceeded       = errors.New("quota exceeded")
	ErrVersionConflict     = errors.New("version conflict")
	ErrCooldown            = errors.New("cooldown in effect")
	ErrUnsupportedRetailer = errors.New("retailer not supported")
)

// FieldError describes one invalid input field.
type FieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ValidationError groups one or more field errors.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		parts = append(parts, f.Field+": "+f.Message)
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// Add appends a field error.
func (e *ValidationError) Add(field, code, msg string) {
	e.Fields = append(e.Fields, FieldError{Field: field, Code: code, Message: msg})
}

// OrNil returns e if it holds any field errors, else nil.
func (e *ValidationError) OrNil() error {
	if len(e.Fields) == 0 {
		return nil
	}
	return e
}

// CooldownError carries how long the caller must wait.
type CooldownError struct {
	RetryAfterSeconds int
}

func (e *CooldownError) Error() string { return "cooldown in effect" }
func (e *CooldownError) Unwrap() error { return ErrCooldown }
