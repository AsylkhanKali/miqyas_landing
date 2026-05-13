package storage

import (
	"bytes"
	"testing"
	"time"
)

// Эти тесты проверяют чистую функцию computeHash без БД.
// Интеграционные тесты с реальным Postgres (Append/VerifyChain) живут
// отдельно под build tag "integration".

func sampleEvent() Event {
	return Event{
		OccurredAt: time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC),
		ActorType:  "service",
		ActorID:    "tender-intel-worker",
		Action:     "tender.synced",
		Resource:   "tender:12345",
		OrgID:      "123456789012",
		TraceID:    "0123456789abcdef0123456789abcdef",
		Metadata:   map[string]any{"page": 0, "items": 100},
	}
}

func TestComputeHash_Deterministic(t *testing.T) {
	e := sampleEvent()
	prev := make([]byte, 32)
	a := computeHash(e, prev)
	b := computeHash(e, prev)
	if !bytes.Equal(a, b) {
		t.Fatalf("hash not deterministic: %x vs %x", a, b)
	}
	if len(a) != 32 {
		t.Fatalf("expected 32 byte hash, got %d", len(a))
	}
}

func TestComputeHash_PrevHashAffectsOutput(t *testing.T) {
	e := sampleEvent()
	zero := make([]byte, 32)
	other := bytes.Repeat([]byte{0xab}, 32)
	h1 := computeHash(e, zero)
	h2 := computeHash(e, other)
	if bytes.Equal(h1, h2) {
		t.Fatalf("hash must depend on prev_hash; got identical")
	}
}

func TestComputeHash_FieldSensitivity(t *testing.T) {
	prev := make([]byte, 32)
	base := computeHash(sampleEvent(), prev)

	cases := []struct {
		name  string
		mutate func(*Event)
	}{
		{"action", func(e *Event) { e.Action = "tender.failed" }},
		{"actor", func(e *Event) { e.ActorID = "other-actor" }},
		{"resource", func(e *Event) { e.Resource = "tender:99999" }},
		{"org_id", func(e *Event) { e.OrgID = "other" }},
		{"metadata", func(e *Event) { e.Metadata = map[string]any{"page": 1} }},
		{"timestamp", func(e *Event) { e.OccurredAt = e.OccurredAt.Add(time.Second) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := sampleEvent()
			c.mutate(&e)
			h := computeHash(e, prev)
			if bytes.Equal(h, base) {
				t.Fatalf("mutating %s did not change hash", c.name)
			}
		})
	}
}

// Проверяем, что nil и пустая map для Metadata дают одинаковый хэш —
// это инвариант, на котором держится orEmpty().
func TestComputeHash_NilMetadataEqualsEmpty(t *testing.T) {
	prev := make([]byte, 32)
	e1 := sampleEvent()
	e1.Metadata = nil
	e2 := sampleEvent()
	e2.Metadata = map[string]any{}
	if !bytes.Equal(computeHash(e1, prev), computeHash(e2, prev)) {
		t.Fatal("nil and empty metadata must hash identically")
	}
}
