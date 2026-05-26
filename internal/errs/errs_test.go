package errs

import (
	"errors"
	"strings"
	"testing"
)

func TestConstructorsHaveExpectedShape(t *testing.T) {
	cases := []struct {
		err      *AppError
		wantCode string
		wantStat int
	}{
		{Validation("v"), "validation_error", 400},
		{Auth("a"), "auth_error", 401},
		{Forbidden("f"), "forbidden", 403},
		{NotFound("n"), "not_found", 404},
		{RateLimit("r"), "rate_limit_exceeded", 429},
		{Upstream("u"), "upstream_error", 502},
		{NoAccountAvailable(), "no_account_available", 503},
		{Internal("i"), "internal_error", 500},
	}
	for _, c := range cases {
		if c.err.Code != c.wantCode {
			t.Fatalf("%s: got code %q want %q", c.wantCode, c.err.Code, c.wantCode)
		}
		if c.err.Status != c.wantStat {
			t.Fatalf("%s: got status %d want %d", c.wantCode, c.err.Status, c.wantStat)
		}
	}
}

func TestErrorStringIncludesCodeAndMessage(t *testing.T) {
	e := Validation("model required")
	if !strings.Contains(e.Error(), "validation_error") || !strings.Contains(e.Error(), "model required") {
		t.Fatalf("error string lost info: %q", e.Error())
	}
	if Internal("").Error() != "internal_error" {
		t.Fatalf("empty message should fall through to code")
	}
}

func TestMetaCarriesFirstMap(t *testing.T) {
	e := Validation("bad", map[string]any{"field": "model"})
	if e.Meta == nil || e.Meta["field"] != "model" {
		t.Fatalf("meta lost: %+v", e.Meta)
	}
	if Validation("bad").Meta != nil {
		t.Fatalf("expected nil meta when none supplied")
	}
}

func TestErrorsAsCompatibility(t *testing.T) {
	var ae *AppError
	if !errors.As(Auth("nope"), &ae) || ae.Status != 401 {
		t.Fatalf("errors.As should land an *AppError")
	}
}
