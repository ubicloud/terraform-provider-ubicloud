package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// waitStep is one scripted detail-GET reply. unparseable: a 200 whose non-JSON body leaves
// JSON200 nil; jsonNonAnswer: a 200 JSON error envelope the parser fills as a zero-value
// database (JSON200 non-nil, state empty). The last step repeats once the script runs out.
type waitStep struct {
	status        int
	state         string
	transportErr  bool
	unparseable   bool
	jsonNonAnswer bool
}

// scriptedDetailRT replays a reply sequence at the wire boundary (captureRT can only answer
// one fixed response per call, not a creating -> 503 -> running progression).
type scriptedDetailRT struct {
	steps []waitStep
	n     int32
}

func (rt *scriptedDetailRT) calls() int { return int(atomic.LoadInt32(&rt.n)) }

func (rt *scriptedDetailRT) RoundTrip(req *http.Request) (*http.Response, error) {
	i := int(atomic.AddInt32(&rt.n, 1)) - 1
	step := rt.steps[len(rt.steps)-1]
	if i < len(rt.steps) {
		step = rt.steps[i]
	}
	if step.transportErr {
		return nil, errors.New("simulated transient transport blip")
	}
	if step.status == http.StatusOK && step.unparseable {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("not a json body")),
			Request:    req,
		}, nil
	}
	if step.status == http.StatusOK && step.jsonNonAnswer {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"code":503,"message":"simulated json non-answer"}}`)),
			Request:    req,
		}, nil
	}
	var respBody []byte
	if step.status == http.StatusOK {
		sample := sampleDetailedPostgresResponse()
		sample.State = step.state
		respBody, _ = json.Marshal(sample)
	} else {
		respBody = []byte(fmt.Sprintf(`{"error":{"code":%d,"message":"simulated %d"}}`, step.status, step.status))
	}
	return &http.Response{
		StatusCode: step.status,
		Status:     fmt.Sprintf("%d %s", step.status, http.StatusText(step.status)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(respBody)),
		Request:    req,
	}, nil
}

func newScriptedPostgresResource(t *testing.T, steps ...waitStep) (*postgresResource, *scriptedDetailRT) {
	t.Helper()
	rt := &scriptedDetailRT{steps: steps}
	return newPostgresResourceWithRT(t, rt), rt
}

func withTransientBudget(t *testing.T, n int) {
	t.Helper()
	prev := postgresWaitTransientBudget
	postgresWaitTransientBudget = n
	t.Cleanup(func() { postgresWaitTransientBudget = prev })
}

// Four 503s, never three in a row: with budget 2 a cumulative (non-resetting) counter would
// fail on the third, while a budget reset by each successful poll rides through to running.
func TestWaitForPostgresRunningToleratesTransientStatusBlip(t *testing.T) {
	withFastPoll(t)
	withTransientBudget(t, 2)
	r, _ := newScriptedPostgresResource(t,
		waitStep{status: http.StatusOK, state: "creating"},
		waitStep{status: http.StatusServiceUnavailable},
		waitStep{status: http.StatusServiceUnavailable},
		waitStep{status: http.StatusOK, state: "creating"},
		waitStep{status: http.StatusServiceUnavailable},
		waitStep{status: http.StatusServiceUnavailable},
		waitStep{status: http.StatusOK, state: "running"},
	)
	pg, diags := r.waitForPostgresRunning(context.Background(), context.Background(), "pjx", "aws-us-east-1", "tf-acc-pg", time.Minute, "test")
	if diags.HasError() {
		t.Fatalf("transient 503 blips must not fail the wait, got: %v", diags)
	}
	if pg == nil || pg.State != "running" {
		t.Fatalf("expected running pg after the blips cleared, got %+v", pg)
	}
}

// Past the budget the surfaced error is the last 503 (not a generic timeout), after exactly
// budget+1 GETs; the 5s opCtx is only a backstop against a broken budget looping forever.
func TestWaitForPostgresRunningFailsAfterConsecutiveStatusBudget(t *testing.T) {
	withFastPoll(t)
	withTransientBudget(t, 2)
	r, rt := newScriptedPostgresResource(t,
		waitStep{status: http.StatusServiceUnavailable},
		waitStep{status: http.StatusServiceUnavailable},
		waitStep{status: http.StatusServiceUnavailable},
	)
	opCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, diags := r.waitForPostgresRunning(opCtx, context.Background(), "pjx", "aws-us-east-1", "tf-acc-pg", 5*time.Second, "test")
	if !diags.HasError() {
		t.Fatal("a run of 503s past the budget must fail the wait")
	}
	if got := diags[0].Summary(); !strings.Contains(got, "Unexpected HTTP status code") {
		t.Fatalf("expected the status-code summary, got %q", got)
	}
	if got := diags[0].Detail(); !strings.Contains(got, "503") {
		t.Fatalf("expected the last 503 surfaced in the detail, got %q", got)
	}
	if rt.calls() != 3 {
		t.Fatalf("expected budget+1 (3) GETs before giving up, got %d", rt.calls())
	}
}

// The create just returned the row, so a 404 now means it vanished: terminal, never budget-retried.
func TestWaitForPostgresRunningKeeps404Terminal(t *testing.T) {
	withFastPoll(t)
	withTransientBudget(t, 6)
	r, rt := newScriptedPostgresResource(t,
		waitStep{status: http.StatusOK, state: "creating"},
		waitStep{status: http.StatusNotFound},
	)
	_, diags := r.waitForPostgresRunning(context.Background(), context.Background(), "pjx", "aws-us-east-1", "tf-acc-pg", time.Minute, "test")
	if !diags.HasError() {
		t.Fatal("a 404 in the running wait must be terminal")
	}
	if got := diags[0].Summary(); !strings.Contains(got, "Unexpected HTTP status code") {
		t.Fatalf("expected the status-code summary, got %q", got)
	}
	if rt.calls() != 2 {
		t.Fatalf("a 404 must fail immediately (not budget-retry): expected 2 GETs, got %d", rt.calls())
	}
}

func TestWaitForPostgresRunningToleratesTransportBlip(t *testing.T) {
	withFastPoll(t)
	withTransientBudget(t, 2)
	r, _ := newScriptedPostgresResource(t,
		waitStep{transportErr: true},
		waitStep{transportErr: true},
		waitStep{status: http.StatusOK, state: "running"},
	)
	pg, diags := r.waitForPostgresRunning(context.Background(), context.Background(), "pjx", "aws-us-east-1", "tf-acc-pg", time.Minute, "test")
	if diags.HasError() {
		t.Fatalf("transient transport blips must not fail the wait, got: %v", diags)
	}
	if pg == nil || pg.State != "running" {
		t.Fatalf("expected running pg after the transport blips cleared, got %+v", pg)
	}
}

func TestWaitForPostgresRunningFailsAfterConsecutiveTransportBudget(t *testing.T) {
	withFastPoll(t)
	withTransientBudget(t, 2)
	r, rt := newScriptedPostgresResource(t,
		waitStep{transportErr: true},
		waitStep{transportErr: true},
		waitStep{transportErr: true},
	)
	opCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, diags := r.waitForPostgresRunning(opCtx, context.Background(), "pjx", "aws-us-east-1", "tf-acc-pg", 5*time.Second, "test")
	if !diags.HasError() {
		t.Fatal("a run of transport errors past the budget must fail the wait")
	}
	if got := diags[0].Summary(); !strings.Contains(got, "Error polling postgres database while waiting for it to become ready") {
		t.Fatalf("expected the transport-error summary, got %q", got)
	}
	if got := diags[0].Detail(); !strings.Contains(got, "simulated transient transport blip") {
		t.Fatalf("expected the last transport error surfaced, got %q", got)
	}
	if rt.calls() != 3 {
		t.Fatalf("expected budget+1 (3) GETs before giving up, got %d", rt.calls())
	}
}

func TestWaitForPostgresDeletedToleratesTransientStatusBlip(t *testing.T) {
	withFastPoll(t)
	withTransientBudget(t, 2)
	r, _ := newScriptedPostgresResource(t,
		waitStep{status: http.StatusOK, state: "deleting"},
		waitStep{status: http.StatusServiceUnavailable},
		waitStep{status: http.StatusServiceUnavailable},
		waitStep{status: http.StatusOK, state: "deleting"},
		waitStep{status: http.StatusServiceUnavailable},
		waitStep{status: http.StatusServiceUnavailable},
		waitStep{status: http.StatusNotFound},
	)
	diags := r.waitForPostgresDeleted(context.Background(), context.Background(), "pjx", "aws-us-east-1", "tf-acc-pg", time.Minute, "test")
	if diags.HasError() {
		t.Fatalf("transient 503 blips must not fail the delete wait, got: %v", diags)
	}
}

func TestWaitForPostgresDeletedFailsAfterConsecutiveStatusBudget(t *testing.T) {
	withFastPoll(t)
	withTransientBudget(t, 2)
	r, rt := newScriptedPostgresResource(t,
		waitStep{status: http.StatusServiceUnavailable},
		waitStep{status: http.StatusServiceUnavailable},
		waitStep{status: http.StatusServiceUnavailable},
	)
	opCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	diags := r.waitForPostgresDeleted(opCtx, context.Background(), "pjx", "aws-us-east-1", "tf-acc-pg", 5*time.Second, "test")
	if !diags.HasError() {
		t.Fatal("a run of 503s past the budget must fail the delete wait")
	}
	if got := diags[0].Summary(); !strings.Contains(got, "Unexpected HTTP status code") {
		t.Fatalf("expected the status-code summary, got %q", got)
	}
	if got := diags[0].Detail(); !strings.Contains(got, "503") {
		t.Fatalf("expected the last 503 surfaced in the detail, got %q", got)
	}
	if rt.calls() != 3 {
		t.Fatalf("expected budget+1 (3) GETs before giving up, got %d", rt.calls())
	}
}

func TestWaitForPostgresDeletedToleratesTransportBlip(t *testing.T) {
	withFastPoll(t)
	withTransientBudget(t, 2)
	r, _ := newScriptedPostgresResource(t,
		waitStep{transportErr: true},
		waitStep{transportErr: true},
		waitStep{status: http.StatusNotFound},
	)
	diags := r.waitForPostgresDeleted(context.Background(), context.Background(), "pjx", "aws-us-east-1", "tf-acc-pg", time.Minute, "test")
	if diags.HasError() {
		t.Fatalf("transient transport blips must not fail the delete wait, got: %v", diags)
	}
}

// A non-answer 200 must not reset the budget, or a 503/non-answer alternation would ride out
// the whole create timeout; the 503s sit one apart, so exhaustion lands on the sixth GET.
func TestWaitForPostgresRunningInterposition200DoesNotResetBudget(t *testing.T) {
	withFastPoll(t)
	withTransientBudget(t, 2)
	r, rt := newScriptedPostgresResource(t,
		waitStep{status: http.StatusOK, state: "creating"},
		waitStep{status: http.StatusServiceUnavailable},
		waitStep{status: http.StatusOK, unparseable: true},
		waitStep{status: http.StatusServiceUnavailable},
		waitStep{status: http.StatusOK, jsonNonAnswer: true},
		waitStep{status: http.StatusServiceUnavailable},
	)
	opCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, diags := r.waitForPostgresRunning(opCtx, context.Background(), "pjx", "aws-us-east-1", "tf-acc-pg", 5*time.Second, "test")
	if !diags.HasError() {
		t.Fatal("interposition 200s must not let a 503 wall escape the budget")
	}
	if got := diags[0].Detail(); !strings.Contains(got, "503") {
		t.Fatalf("expected the last 503 surfaced in the detail, got %q", got)
	}
	if rt.calls() != 6 {
		t.Fatalf("interposition 200s must not reset the budget: expected exhaustion on the 6th GET, got %d", rt.calls())
	}
}

// Deleted-wait analogue: only a still-present snapshot carrying a state resets the budget.
func TestWaitForPostgresDeletedInterposition200DoesNotResetBudget(t *testing.T) {
	withFastPoll(t)
	withTransientBudget(t, 2)
	r, rt := newScriptedPostgresResource(t,
		waitStep{status: http.StatusOK, state: "deleting"},
		waitStep{status: http.StatusServiceUnavailable},
		waitStep{status: http.StatusOK, unparseable: true},
		waitStep{status: http.StatusServiceUnavailable},
		waitStep{status: http.StatusOK, jsonNonAnswer: true},
		waitStep{status: http.StatusServiceUnavailable},
	)
	opCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	diags := r.waitForPostgresDeleted(opCtx, context.Background(), "pjx", "aws-us-east-1", "tf-acc-pg", 5*time.Second, "test")
	if !diags.HasError() {
		t.Fatal("interposition 200s must not let a 503 wall escape the delete budget")
	}
	if got := diags[0].Detail(); !strings.Contains(got, "503") {
		t.Fatalf("expected the last 503 surfaced in the detail, got %q", got)
	}
	if rt.calls() != 6 {
		t.Fatalf("interposition 200s must not reset the budget: expected exhaustion on the 6th GET, got %d", rt.calls())
	}
}

// When the budget never exhausts, the deadline is the real reason the wait ended:
// classify as Timeout, not as the incidental last 503.
func TestWaitForPostgresRunningTransientStreamStillTimesOut(t *testing.T) {
	withFastPoll(t)
	withTransientBudget(t, 1_000_000)
	r, _ := newScriptedPostgresResource(t,
		waitStep{status: http.StatusServiceUnavailable},
	)
	opCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, diags := r.waitForPostgresRunning(opCtx, context.Background(), "pjx", "aws-us-east-1", "tf-acc-pg", 20*time.Millisecond, "test")
	if !diags.HasError() {
		t.Fatal("a transient stream to the deadline must fail")
	}
	if got := diags[0].Summary(); !strings.Contains(got, "Timeout") {
		t.Fatalf("a tolerated transient at the deadline must classify as a timeout, got %q", got)
	}
	if got := diags[0].Summary(); strings.Contains(got, "Unexpected HTTP status code") {
		t.Fatalf("a tolerated transient must not surface as a hard unexpected-status error, got %q", got)
	}
}
