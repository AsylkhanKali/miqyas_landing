// Тесты SubmissionWorkflow через go.temporal.io/sdk/testsuite.
//
// Цель — зафиксировать главные инварианты:
//   1. Happy path: draft → reviewed → signed → submitted → acknowledged → archived.
//   2. Сигнал submit ВНУТРИ окна T-30 минут без acknowledge_cutoff — отклоняется.
//   3. Сигнал submit ВНУТРИ окна с acknowledge_cutoff=true — проходит.
//   4. Cancel из любого pre-submitted состояния — корректно завершает.
//   5. Дедлайн в прошлом — failed.
package workflows

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/testsuite"

	"github.com/goszakup/platform/internal/submission/domain"
)

// trackingActivities — фейковая реализация активностей: записывает
// все переходы и эмулирует успешный submit. Без сетевых вызовов.
type trackingActivities struct {
	transitions []RecordTransitionParams
	submitErr   error
}

func (a *trackingActivities) RecordTransitionActivity(_ context.Context, p RecordTransitionParams) error {
	a.transitions = append(a.transitions, p)
	return nil
}

func (a *trackingActivities) SubmitToPlatformActivity(_ context.Context, p SubmitActivityParams) (SubmitActivityResult, error) {
	if a.submitErr != nil {
		return SubmitActivityResult{}, a.submitErr
	}
	return SubmitActivityResult{
		ReceiptID:  "FAKE-" + p.IdempotencyKey,
		AcceptedAt: time.Now().UTC(),
	}, nil
}

func newEnv(t *testing.T, acts *trackingActivities) (*testsuite.TestWorkflowEnvironment, *testsuite.WorkflowTestSuite) {
	t.Helper()
	s := &testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	env.RegisterActivity(acts.RecordTransitionActivity)
	env.RegisterActivity(acts.SubmitToPlatformActivity)
	return env, s
}

func startParams(deadline time.Time) StartParams {
	return StartParams{
		SubmissionID:    uuid.New(),
		OrgID:           "123456789012",
		TenderID:        "T-1",
		Platform:        "goszakup",
		DocumentID:      uuid.New(),
		DocumentVersion: 1,
		DeadlineAt:      deadline.UTC().Format(time.RFC3339),
	}
}

func lastState(acts *trackingActivities) string {
	if len(acts.transitions) == 0 {
		return ""
	}
	return acts.transitions[len(acts.transitions)-1].To
}

func countTransitions(acts *trackingActivities, to string) int {
	n := 0
	for _, tr := range acts.transitions {
		if tr.To == to {
			n++
		}
	}
	return n
}

// ── 1. Happy path ─────────────────────────────────────────────────────────

func TestWorkflow_HappyPath(t *testing.T) {
	acts := &trackingActivities{}
	env, _ := newEnv(t, acts)

	// Дедлайн через 24 часа — далеко от окна cutoff.
	deadline := time.Now().Add(24 * time.Hour)

	// Через короткие задержки шлём сигналы по очереди.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalReview, ReviewSignal{Actor: "alice@example.kz"})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSign, SignSignal{Actor: "bob@example.kz", ESIGCertCN: "CN=B", ESIGCertSHA: "deadbeef"})
	}, 2*time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSubmit, SubmitSignal{Actor: "bob@example.kz", IdempotencyKey: "k1"})
	}, 3*time.Millisecond)

	env.ExecuteWorkflow(SubmissionWorkflow, startParams(deadline))

	if !env.IsWorkflowCompleted() {
		t.Fatalf("workflow not completed")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow returned error: %v", err)
	}
	var res Result
	if err := env.GetWorkflowResult(&res); err != nil {
		t.Fatal(err)
	}
	if res.FinalState != string(domain.StateArchived) {
		t.Fatalf("expected archived, got %s", res.FinalState)
	}
	if res.ReceiptID == "" {
		t.Fatal("expected receipt id")
	}
}

// ── 2. Cutoff window: submit без acknowledge отклоняется ──────────────────

func TestWorkflow_CutoffWithoutAck_Rejected(t *testing.T) {
	acts := &trackingActivities{}
	env, _ := newEnv(t, acts)

	// Дедлайн через 10 минут (внутри окна 30 минут).
	deadline := time.Now().Add(10 * time.Minute)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalReview, ReviewSignal{Actor: "alice@example.kz"})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSign, SignSignal{Actor: "bob@example.kz"})
	}, 2*time.Millisecond)
	env.RegisterDelayedCallback(func() {
		// Без acknowledge_cutoff — должно быть отклонено.
		env.SignalWorkflow(SignalSubmit, SubmitSignal{Actor: "bob@example.kz", IdempotencyKey: "k1"})
	}, 3*time.Millisecond)
	env.RegisterDelayedCallback(func() {
		// После отказа отправляем cancel, чтобы workflow завершился.
		env.SignalWorkflow(SignalCancel, CancelSignal{Actor: "bob@example.kz", Reason: "give_up"})
	}, 5*time.Millisecond)

	env.ExecuteWorkflow(SubmissionWorkflow, startParams(deadline))

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow not completed")
	}
	// Должна быть запись о rejection: переход signed → signed с reason 'submit_rejected_cutoff_window'.
	foundReject := false
	for _, tr := range acts.transitions {
		if tr.Reason == "submit_rejected_cutoff_window" {
			foundReject = true
		}
	}
	if !foundReject {
		t.Fatal("expected a submit_rejected_cutoff_window transition")
	}
	if lastState(acts) != string(domain.StateCancelled) {
		t.Fatalf("expected final state cancelled, got %s", lastState(acts))
	}
	if countTransitions(acts, string(domain.StateSubmitted)) != 0 {
		t.Fatal("workflow must not have submitted inside cutoff without ack")
	}
}

// ── 3. Cutoff с acknowledge: подача проходит ──────────────────────────────

func TestWorkflow_CutoffWithAck_Proceeds(t *testing.T) {
	acts := &trackingActivities{}
	env, _ := newEnv(t, acts)

	deadline := time.Now().Add(10 * time.Minute) // внутри окна

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalReview, ReviewSignal{Actor: "alice@example.kz"})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSign, SignSignal{Actor: "bob@example.kz"})
	}, 2*time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSubmit, SubmitSignal{
			Actor:             "bob@example.kz",
			IdempotencyKey:    "k1",
			AcknowledgeCutoff: true,
		})
	}, 3*time.Millisecond)

	env.ExecuteWorkflow(SubmissionWorkflow, startParams(deadline))

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow not completed")
	}
	var res Result
	_ = env.GetWorkflowResult(&res)
	if res.FinalState != string(domain.StateArchived) {
		t.Fatalf("expected archived, got %s; transitions=%+v", res.FinalState, acts.transitions)
	}
}

// ── 4. Cancel из reviewed ─────────────────────────────────────────────────

func TestWorkflow_CancelFromReviewed(t *testing.T) {
	acts := &trackingActivities{}
	env, _ := newEnv(t, acts)
	deadline := time.Now().Add(24 * time.Hour)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalReview, ReviewSignal{Actor: "alice@example.kz"})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalCancel, CancelSignal{Actor: "alice@example.kz", Reason: "wrong_lot"})
	}, 2*time.Millisecond)

	env.ExecuteWorkflow(SubmissionWorkflow, startParams(deadline))

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow not completed")
	}
	var res Result
	_ = env.GetWorkflowResult(&res)
	if !res.Cancelled || res.FinalState != string(domain.StateCancelled) {
		t.Fatalf("expected cancelled, got %+v", res)
	}
}

// ── 5. Дедлайн в прошлом ──────────────────────────────────────────────────

func TestWorkflow_DeadlinePassed(t *testing.T) {
	acts := &trackingActivities{}
	env, _ := newEnv(t, acts)
	deadline := time.Now().Add(-1 * time.Hour) // уже прошёл

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalReview, ReviewSignal{Actor: "alice@example.kz"})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSign, SignSignal{Actor: "bob@example.kz"})
	}, 2*time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSubmit, SubmitSignal{Actor: "bob@example.kz", IdempotencyKey: "k1"})
	}, 3*time.Millisecond)

	env.ExecuteWorkflow(SubmissionWorkflow, startParams(deadline))

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow not completed")
	}
	var res Result
	_ = env.GetWorkflowResult(&res)
	if res.FinalState != string(domain.StateFailed) {
		t.Fatalf("expected failed, got %s", res.FinalState)
	}
	if countTransitions(acts, string(domain.StateSubmitted)) != 0 {
		t.Fatal("must not submit past deadline")
	}
}

