package db

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestValidateIdentifier(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"", "docmatch_app", "App1", "_x"} {
		if err := validateIdentifier(ok); err != nil {
			t.Errorf("%q deveria ser válido: %v", ok, err)
		}
	}
	for _, bad := range []string{"1abc", "a b", "a;b", "a-b", string(make([]byte, 64))} {
		if err := validateIdentifier(bad); err == nil {
			t.Errorf("%q deveria ser inválido", bad)
		}
	}
}

func TestConnectRejectsBadInput(t *testing.T) {
	t.Parallel()
	if _, err := Connect(context.Background(), Options{}); err == nil {
		t.Fatal("URL vazia deveria falhar")
	}
	if _, err := Connect(context.Background(), Options{URL: "postgres://x", AppRole: "bad role"}); err == nil {
		t.Fatal("papel inválido deveria falhar")
	}
	if _, err := Connect(context.Background(), Options{URL: "://nope"}); err == nil {
		t.Fatal("URL inválida deveria falhar")
	}
}

func TestRedactURL(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"postgres://user:senha@localhost:5432/db?sslmode=disable": "postgres://user:***@localhost:5432/db?sslmode=disable",
		"postgres://user@localhost/db":                            "postgres://user@localhost/db",
		"postgres://localhost/db":                                 "postgres://localhost/db",
		"sem-esquema":                                             "sem-esquema",
	}
	for in, want := range cases {
		if got := RedactURL(in); got != want {
			t.Errorf("RedactURL(%q) = %q, esperado %q", in, got, want)
		}
	}
}

func TestErrorClassifiers(t *testing.T) {
	t.Parallel()
	if !IsUniqueViolation(&pgconn.PgError{Code: "23505"}) || IsUniqueViolation(errors.New("x")) {
		t.Fatal("IsUniqueViolation")
	}
	if !IsCheckViolation(&pgconn.PgError{Code: "23514"}) || IsCheckViolation(nil) {
		t.Fatal("IsCheckViolation")
	}
	if !IsRLSViolation(&pgconn.PgError{Code: "42501"}) || IsRLSViolation(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("IsRLSViolation")
	}
}

func TestWithOrgTxRejectsNilOrg(t *testing.T) {
	t.Parallel()
	err := WithOrgTx(context.Background(), failingBeginner{}, uuid.Nil, func(context.Context, pgx.Tx) error { return nil })
	if err == nil {
		t.Fatal("org nulo deveria falhar antes de abrir transação")
	}
}

func TestWithTxBeginError(t *testing.T) {
	t.Parallel()
	err := WithSystemTx(context.Background(), failingBeginner{}, func(context.Context, pgx.Tx) error { return nil })
	if err == nil || !errors.Is(err, errBegin) {
		t.Fatalf("erro de begin deveria propagar: %v", err)
	}
}

var errBegin = errors.New("sem conexão")

type failingBeginner struct{}

func (failingBeginner) Begin(context.Context) (pgx.Tx, error) { return nil, errBegin }
