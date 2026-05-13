package validator

import (
	"testing"

	"github.com/goszakup/platform/internal/document/domain"
)

func TestValidate_SchemaPass(t *testing.T) {
	v := New()
	tmpl := domain.Template{
		Schema: map[string]any{
			"type":     "object",
			"required": []any{"amount", "currency"},
			"properties": map[string]any{
				"amount":   map[string]any{"type": "number", "minimum": 0},
				"currency": map[string]any{"type": "string", "enum": []any{"KZT", "USD"}},
			},
		},
	}
	res, err := v.Validate(tmpl, map[string]any{"amount": 1000.0, "currency": "KZT"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected valid, got errors: %+v", res.Errors)
	}
}

func TestValidate_SchemaFail_MissingRequired(t *testing.T) {
	v := New()
	tmpl := domain.Template{
		Schema: map[string]any{
			"type":     "object",
			"required": []any{"amount"},
			"properties": map[string]any{
				"amount": map[string]any{"type": "number"},
			},
		},
	}
	res, _ := v.Validate(tmpl, map[string]any{"currency": "KZT"})
	if res.Valid {
		t.Fatal("expected invalid for missing required field")
	}
	if len(res.Errors) == 0 {
		t.Fatal("expected at least one error")
	}
}

func TestValidate_MinAmountRule(t *testing.T) {
	v := New()
	tmpl := domain.Template{
		Schema: map[string]any{"type": "object"},
		Rules: []domain.Rule{
			{Kind: "min_amount", Params: map[string]any{"field": "amount", "min": 1000.0}},
		},
	}

	res, _ := v.Validate(tmpl, map[string]any{"amount": 500.0})
	if res.Valid {
		t.Fatal("expected fail for amount below min")
	}

	res, _ = v.Validate(tmpl, map[string]any{"amount": 1500.0})
	if !res.Valid {
		t.Fatalf("expected pass, got: %+v", res.Errors)
	}
}

func TestValidate_BINRule(t *testing.T) {
	v := New()
	tmpl := domain.Template{
		Schema: map[string]any{"type": "object"},
		Rules: []domain.Rule{
			{Kind: "bin", Params: map[string]any{"field": "bin"}},
		},
	}

	cases := []struct {
		bin   string
		valid bool
	}{
		{"123456789012", true},
		{"12345678901", false},   // 11 цифр
		{"1234567890123", false}, // 13 цифр
		{"12345678abcd", false},  // буквы
		{"", false},
	}
	for _, c := range cases {
		res, _ := v.Validate(tmpl, map[string]any{"bin": c.bin})
		if res.Valid != c.valid {
			t.Errorf("bin=%q: expected valid=%v, got %v (errors: %+v)", c.bin, c.valid, res.Valid, res.Errors)
		}
	}
}

func TestValidate_DeadlineBeforeRule(t *testing.T) {
	v := New()
	tmpl := domain.Template{
		Schema: map[string]any{"type": "object"},
		Rules: []domain.Rule{
			{Kind: "deadline_before", Params: map[string]any{
				"field":  "submit_by",
				"before": "2026-06-01T12:00:00Z",
			}},
		},
	}

	res, _ := v.Validate(tmpl, map[string]any{"submit_by": "2026-05-15T12:00:00Z"})
	if !res.Valid {
		t.Fatalf("expected pass (date before boundary), got: %+v", res.Errors)
	}

	res, _ = v.Validate(tmpl, map[string]any{"submit_by": "2026-07-01T12:00:00Z"})
	if res.Valid {
		t.Fatal("expected fail (date after boundary)")
	}
}

func TestValidate_UnknownRule(t *testing.T) {
	v := New()
	tmpl := domain.Template{
		Schema: map[string]any{"type": "object"},
		Rules:  []domain.Rule{{Kind: "nope"}},
	}
	res, _ := v.Validate(tmpl, map[string]any{})
	if res.Valid {
		t.Fatal("unknown rule should yield validation error, not silent pass")
	}
}
