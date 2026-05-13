package aggregator

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/goszakup/platform/internal/console/clients"
)

func TestAllowedActionsFor(t *testing.T) {
	cases := []struct {
		state        string
		insideCutoff bool
		want         []string
	}{
		{"draft", false, []string{"review", "cancel"}},
		{"draft", true, []string{"review", "cancel"}},
		{"reviewed", false, []string{"sign", "cancel"}},
		{"signed", false, []string{"submit", "cancel"}},
		{"signed", true, []string{"submit_with_ack", "cancel"}},
		{"submitted", false, []string{}},
		{"acknowledged", false, []string{}},
		{"archived", false, []string{}},
		{"cancelled", false, []string{}},
		{"failed", false, []string{}},
		{"unknown", false, []string{}},
	}
	for _, c := range cases {
		got := allowedActionsFor(c.state, c.insideCutoff)
		if !equalStringSets(got, c.want) {
			t.Errorf("state=%s inside=%v: want %v, got %v", c.state, c.insideCutoff, c.want, got)
		}
	}
}

func TestComputeHints_OutsideCutoff(t *testing.T) {
	sub := clients.SubmissionDTO{
		State:      "signed",
		DeadlineAt: time.Now().Add(24 * time.Hour),
	}
	h := computeHints(sub)
	if h.InsideCutoffWindow {
		t.Fatal("should not be inside cutoff for 24h deadline")
	}
	if !contains(h.AllowedActions, "submit") {
		t.Fatalf("expected submit allowed, got %+v", h.AllowedActions)
	}
}

func TestComputeHints_InsideCutoff(t *testing.T) {
	sub := clients.SubmissionDTO{
		State:      "signed",
		DeadlineAt: time.Now().Add(15 * time.Minute),
	}
	h := computeHints(sub)
	if !h.InsideCutoffWindow {
		t.Fatal("should be inside cutoff for 15m deadline")
	}
	if !contains(h.AllowedActions, "submit_with_ack") {
		t.Fatalf("expected submit_with_ack, got %+v", h.AllowedActions)
	}
	if contains(h.AllowedActions, "submit") {
		t.Fatal("plain submit must NOT be allowed inside cutoff")
	}
}

func TestComputeHints_DeadlinePassed(t *testing.T) {
	sub := clients.SubmissionDTO{
		State:      "signed",
		DeadlineAt: time.Now().Add(-time.Hour),
	}
	h := computeHints(sub)
	if h.UntilDeadlineSeconds > 0 {
		t.Fatalf("expected negative or zero seconds, got %d", h.UntilDeadlineSeconds)
	}
	// При истёкшем дедлайне inside_cutoff_window=false (по нашему правилу
	// until > 0 && until <= cutoff).
	if h.InsideCutoffWindow {
		t.Fatal("inside_cutoff_window must be false past deadline")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func equalStringSets(a, b []string) bool {
	ac := append([]string{}, a...)
	bc := append([]string{}, b...)
	sort.Strings(ac)
	sort.Strings(bc)
	return reflect.DeepEqual(ac, bc)
}
