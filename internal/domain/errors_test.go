package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestNormalizeEmail(t *testing.T) {
	result, err := NormalizeEmail("  PERSON@Example.com ")
	if err != nil || result != "person@example.com" {
		t.Fatalf("normalization failed: %q", result)
	}
	for _, input := range []string{"", "not-email", "Name <a@example.com>", "a@example.com,b@example.com", strings.Repeat("a", 255) + "@example.com"} {
		if _, err := NormalizeEmail(input); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted invalid email %q", input)
		}
	}
}

func TestNormalizeName(t *testing.T) {
	if result, err := NormalizeName("  Tester  "); err != nil || result != "Tester" {
		t.Fatal("normalization failed")
	}
	for _, input := range []string{"", "   ", strings.Repeat("x", 101), string([]byte{0xff})} {
		if _, err := NormalizeName(input); !errors.Is(err, ErrInvalid) {
			t.Fatal("accepted invalid name")
		}
	}
}

func TestDatabaseError(t *testing.T) {
	if !errors.Is(DatabaseError(pgx.ErrNoRows), ErrNotFound) {
		t.Fatal("no rows should map to not found")
	}
	if !errors.Is(DatabaseError(&pgconn.PgError{Code: "23505"}), ErrConflict) {
		t.Fatal("unique violation should map to conflict")
	}
	original := errors.New("connection failed")
	if DatabaseError(original) != original {
		t.Fatal("unexpected error conversion")
	}
}
