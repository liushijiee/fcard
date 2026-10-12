package domain

import (
	"errors"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrInvalid                = errors.New("invalid input")
	ErrUnauthorized           = errors.New("invalid credentials or session")
	ErrConflict               = errors.New("resource already exists")
	ErrNotFound               = errors.New("resource not found")
	ErrRateLimited            = errors.New("too many login attempts")
	ErrPasswordChangeRequired = errors.New("password change required")
	ErrInvalidState           = errors.New("resource state does not allow this operation")
	ErrIssuingDisabled        = errors.New("sandbox issuing is disabled")
	ErrCardholderRequired     = errors.New("a verified cardholder mapping is required")
	ErrCardLimit              = errors.New("sandbox card count limit reached")
	ErrProviderUnavailable    = errors.New("provider request failed")
	ErrIdempotencyConflict    = errors.New("idempotency key already used with different input")
)

func NormalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 254 || !strings.Contains(email, "@") {
		return "", ErrInvalid
	}
	return email, nil
}

func NormalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 100 {
		return "", ErrInvalid
	}
	return name, nil
}

func DatabaseError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return ErrConflict
	}
	return err
}
