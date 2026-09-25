package errors

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"

	"github.com/go-playground/validator/v10"
	_ "github.com/tigerbeetle/tigerbeetle-go/pkg/errors"
	_ "github.com/tigerbeetle/tigerbeetle-go/pkg/types"
)

type ErrorType string

const (
	ErrNotFound         ErrorType = "ENTRY_NOT_FOUND_ERROR"
	ErrValidation       ErrorType = "VALIDATION_ERROR"
	ErrEntryExists      ErrorType = "ENTRY_EXISTS_ERROR"
	ErrEntryDeleted     ErrorType = "ENTRY_DELETED_ERROR"
	ErrAuthorization    ErrorType = "AUTHORIZATION_ERROR"
	ErrExpiredToken     ErrorType = "EXPIRED_TOKEN_ERROR"
	ErrAuthentication   ErrorType = "AUTHENTICATION_ERROR"
	ErrInvalidToken     ErrorType = "INVALID_TOKEN_ERROR"
	ErrPermission       ErrorType = "PERMISSION_ERROR"
	ErrFailedDependency ErrorType = "FAILED_DEPENDENCY"
	ErrFatal            ErrorType = "FATAL_ERROR"
	ErrNotImplemented   ErrorType = "NOT_IMPLEMENTED_ERROR"
)

type AppError struct {
	Code     int       `json:"-"`
	Status   string    `json:"status"`
	Type     ErrorType `json:"type"`
	Message  string    `json:"message"`
	Internal string    `json:"internal,omitempty"`
}

func (a AppError) Error() string {
	return fmt.Sprintf("%s: %s", a.Type, a.Message)
}

func (a AppError) Serialize(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(a.Code)
	if err := json.NewEncoder(w).Encode(a); err != nil {
		// TODO: panic err instead?
		// ?
		panic(a)
	}
}

func Is(err, target error) bool {
	return errors.Is(err, target)
}

func HandleDataDBError(err error) AppError {
	if Is(err, sql.ErrNoRows) {
		return NewNotFoundError("resource not found")
	}
	return NewFatalError(err)
}

func HandleTxDBError(err error) AppError {
	// todo
	return NewFatalError(err)
}

func HandleBindError(err error) AppError {
	if errors.As(err, &AppError{}) {
		return AsAppError(err)
	}

	if v, ok := err.(validator.ValidationErrors); ok {
		var message string
		switch v[0].ActualTag() {
		case "required":
			message = fmt.Sprintf("%s is required", v[0].Field())
		case "required_without":
			message = fmt.Sprintf("%s is required when %s is not provided", v[0].Field(), v[0].Param())
		case "oneof":
			message = fmt.Sprintf("%s must be one of values: (%s), value received: %s", v[0].Field(), v[0].Param(), v[0].Value())
		case "gt":
			message = fmt.Sprintf("%s must be greater than (%s), value received: %s", v[0].Field(), v[0].Param(), v[0].Value())
		default:
			message = fmt.Sprintf("Validation failed on field { %s }, Condition: %s", v[0].Field(), v[0].ActualTag())
			if v[0].Param() != "" {
				message += fmt.Sprintf("{ %s }", v[0].Param())
			}
			if v[0].Value() != "" && v[0].Value() != nil {
				message += fmt.Sprintf(", Value Recieved: %v", v[0].Value())
			}
		}

		return AppError{
			Code:     http.StatusBadRequest,
			Type:     ErrValidation,
			Message:  message,
			Internal: err.Error(),
		}
	}
	if Is(err, io.EOF) {
		return NewValidationError("No request body")
	}

	vErr := NewValidationError("invalid request received")
	vErr.Internal = err.Error()

	return vErr
}

func NewValidationError(msg string) AppError {
	return AppError{
		Code:    http.StatusBadRequest,
		Type:    ErrValidation,
		Message: msg,
	}
}

func NewNotFoundError(msg string) AppError {
	return AppError{
		Code:    http.StatusNotFound,
		Type:    ErrNotFound,
		Message: msg,
	}
}

func NewPermissionError(msg string) AppError {
	return AppError{
		Code:    http.StatusForbidden,
		Type:    ErrPermission,
		Message: msg,
	}
}

func NewAuthenticationError(msg string) AppError {
	return AppError{
		Code:    http.StatusUnauthorized,
		Type:    ErrAuthentication,
		Message: msg,
	}
}

func NewInvalidTokenError() AppError {
	return AppError{
		Code:    http.StatusUnauthorized,
		Type:    ErrInvalidToken,
		Message: "Invalid token",
	}
}

func NewFatalError(err error) AppError {
	debug.PrintStack()
	return AppError{
		Status:   "error",
		Code:     http.StatusInternalServerError,
		Type:     ErrFatal,
		Message:  "Oops! something happened on our end.",
		Internal: err.Error(),
	}
}

func NewUnknownError(err any) AppError {
	return NewFatalError(fmt.Errorf("%v", err))
}

func NewFailedDependencyError(msg string) AppError {
	return AppError{
		Code:    http.StatusFailedDependency,
		Type:    ErrFailedDependency,
		Message: msg,
	}
}

func NewImplementationError() AppError {
	return AppError{
		Code:    http.StatusNotImplemented,
		Type:    ErrNotImplemented,
		Message: "functionality not implemented requires additional information",
	}
}

func AsAppError(err error) AppError {
	apperr := new(AppError)
	if errors.As(err, apperr) {
		apperr.Status = "error"
		return *apperr
	}
	return NewFatalError(err)
}
