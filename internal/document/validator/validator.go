// Package validator проверяет payload документа по двум слоям:
//
//   1) JSON Schema — структурная валидация (типы, required, форматы).
//   2) Доменные правила — простые предикаты, специфичные для закупок РК
//      (минимальная сумма, дедлайн до даты, BIN — 12 цифр, и т.п.).
//
// Слои применяются независимо; результаты объединяются.
package validator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/goszakup/platform/internal/document/domain"
)

type Validator struct{}

func New() *Validator { return &Validator{} }

// Validate возвращает результат валидации; ошибка возвращается только при
// внутренних проблемах (некорректная схема). Бизнес-ошибки попадают в
// ValidationResult.Errors.
func (v *Validator) Validate(t domain.Template, payload map[string]any) (domain.ValidationResult, error) {
	res := domain.ValidationResult{Valid: true}

	// 1) JSON Schema
	if len(t.Schema) > 0 {
		schemaBytes, err := json.Marshal(t.Schema)
		if err != nil {
			return res, fmt.Errorf("marshal schema: %w", err)
		}
		c := jsonschema.NewCompiler()
		if err := c.AddResource("inline://schema.json", bytes.NewReader(schemaBytes)); err != nil {
			return res, fmt.Errorf("compile add: %w", err)
		}
		sch, err := c.Compile("inline://schema.json")
		if err != nil {
			return res, fmt.Errorf("compile: %w", err)
		}
		if err := sch.Validate(payload); err != nil {
			if verr, ok := err.(*jsonschema.ValidationError); ok {
				for _, cause := range flatten(verr) {
					res.Errors = append(res.Errors, domain.ValidationError{
						Path:    cause.InstanceLocation,
						Message: cause.Message,
					})
				}
			} else {
				res.Errors = append(res.Errors, domain.ValidationError{Message: err.Error()})
			}
		}
	}

	// 2) Доменные правила
	for _, rule := range t.Rules {
		if err := applyRule(rule, payload); err != nil {
			res.Errors = append(res.Errors, *err)
		}
	}

	res.Valid = len(res.Errors) == 0
	return res, nil
}

func flatten(v *jsonschema.ValidationError) []*jsonschema.ValidationError {
	if len(v.Causes) == 0 {
		return []*jsonschema.ValidationError{v}
	}
	out := make([]*jsonschema.ValidationError, 0, len(v.Causes))
	for _, c := range v.Causes {
		out = append(out, flatten(c)...)
	}
	return out
}

// applyRule возвращает ошибку валидации, если правило не выполнено.
// Возвращаемый указатель nil означает успешное прохождение.
func applyRule(r domain.Rule, payload map[string]any) *domain.ValidationError {
	switch r.Kind {
	case "required":
		field, _ := r.Params["field"].(string)
		if field == "" {
			return nil
		}
		if _, ok := payload[field]; !ok {
			return &domain.ValidationError{
				Path:    "/" + field,
				Message: fmt.Sprintf("field %q is required by domain rule", field),
			}
		}

	case "min_amount":
		field, _ := r.Params["field"].(string)
		min, _ := toFloat(r.Params["min"])
		v, _ := toFloat(payload[field])
		if v < min {
			return &domain.ValidationError{
				Path:    "/" + field,
				Message: fmt.Sprintf("amount %.2f is below required minimum %.2f", v, min),
			}
		}

	case "deadline_before":
		field, _ := r.Params["field"].(string)
		boundary, _ := r.Params["before"].(string)
		raw, _ := payload[field].(string)
		if raw == "" || boundary == "" {
			return nil
		}
		t, err1 := parseTime(raw)
		b, err2 := parseTime(boundary)
		if err1 != nil || err2 != nil {
			return &domain.ValidationError{Path: "/" + field, Message: "unparseable date"}
		}
		if !t.Before(b) {
			return &domain.ValidationError{
				Path:    "/" + field,
				Message: fmt.Sprintf("date %s must be before %s", raw, boundary),
			}
		}

	case "bin":
		field, _ := r.Params["field"].(string)
		raw, _ := payload[field].(string)
		if len(raw) != 12 || strings.TrimLeft(raw, "0123456789") != "" {
			return &domain.ValidationError{
				Path:    "/" + field,
				Message: "BIN must be exactly 12 digits",
			}
		}

	default:
		return &domain.ValidationError{
			Path:    "/",
			Message: fmt.Sprintf("unknown rule kind: %s", r.Kind),
		}
	}
	return nil
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

func parseTime(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time: %s", s)
}
