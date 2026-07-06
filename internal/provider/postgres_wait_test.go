package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"
)

// newTestPostgresResource drives the real generated client against an httptest
// backend (a network-boundary fake), never a stub of the client.
func newTestPostgresResource(t *testing.T, srv *httptest.Server) *postgresResource {
	t.Helper()
	client, err := ubicloud_client.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithResponses: %v", err)
	}
	return &postgresResource{uc: &UbicloudClient{client: client}}
}

// detailsHandler serves the detailed GET in the state next() returns; "" answers a 404.
func detailsHandler(next func() string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state := next()
		if state == "" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
			return
		}
		resp := sampleDetailedPostgresResponse()
		resp.State = state
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func withFastPoll(t *testing.T) {
	t.Helper()
	prev := postgresPollInterval
	postgresPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { postgresPollInterval = prev })
}

func TestWaitForPostgresRunningPollsUntilRunning(t *testing.T) {
	withFastPoll(t)
	var calls int32
	srv := httptest.NewServer(detailsHandler(func() string {
		if atomic.AddInt32(&calls, 1) >= 3 {
			return "running"
		}
		return "creating"
	}))
	defer srv.Close()

	r := newTestPostgresResource(t, srv)
	pg, diags := r.waitForPostgresRunning(context.Background(), context.Background(), "pjx", "aws-us-east-1", "tf-acc-pg", time.Minute, "test")
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if pg == nil || pg.State != "running" {
		t.Fatalf("expected running pg, got %+v", pg)
	}
	if got := atomic.LoadInt32(&calls); got < 3 {
		t.Fatalf("expected at least 3 polls, got %d", got)
	}
}

func TestWaitForPostgresRunningTimesOut(t *testing.T) {
	withFastPoll(t)
	srv := httptest.NewServer(detailsHandler(func() string { return "creating" }))
	defer srv.Close()

	r := newTestPostgresResource(t, srv)
	opCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	pg, diags := r.waitForPostgresRunning(opCtx, context.Background(), "pjx", "aws-us-east-1", "tf-acc-pg", 20*time.Millisecond, "test")
	if !diags.HasError() {
		t.Fatal("expected a timeout error, got none")
	}
	if !strings.Contains(diags[0].Summary(), "Timeout") {
		t.Fatalf("expected timeout summary, got %q", diags[0].Summary())
	}
	// The last observed (creating) snapshot is returned so Create can persist partial state.
	if pg == nil || pg.State != "creating" {
		t.Fatalf("expected last creating snapshot, got %+v", pg)
	}
}

func TestWaitForPostgresRunningHonorsContextCancel(t *testing.T) {
	withFastPoll(t)
	srv := httptest.NewServer(detailsHandler(func() string { return "creating" }))
	defer srv.Close()

	parentCtx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: the helper must not spin to the deadline
	opCtx, opCancel := context.WithTimeout(parentCtx, time.Hour)
	defer opCancel()

	r := newTestPostgresResource(t, srv)
	_, diags := r.waitForPostgresRunning(opCtx, parentCtx, "pjx", "aws-us-east-1", "tf-acc-pg", time.Hour, "test")
	if !diags.HasError() {
		t.Fatal("expected a cancellation error, got none")
	}
	// Parent cancellation must classify as Cancelled, not Timeout: raising timeouts.create cannot help.
	if got := diags[0].Summary(); !strings.Contains(got, "Cancelled") {
		t.Fatalf("expected a cancellation summary, got %q", got)
	}
}

func TestWaitForPostgresDeletedHonorsContextCancel(t *testing.T) {
	withFastPoll(t)
	srv := httptest.NewServer(detailsHandler(func() string { return "deleting" }))
	defer srv.Close()

	parentCtx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	opCtx, opCancel := context.WithTimeout(parentCtx, time.Hour)
	defer opCancel()

	r := newTestPostgresResource(t, srv)
	diags := r.waitForPostgresDeleted(opCtx, parentCtx, "pjx", "aws-us-east-1", "tf-acc-pg", time.Hour, "test")
	if !diags.HasError() {
		t.Fatal("expected a cancellation error, got none")
	}
	if got := diags[0].Summary(); !strings.Contains(got, "Cancelled") {
		t.Fatalf("expected a cancellation summary, got %q", got)
	}
}

func TestWaitForPostgresDeletedPollsUntilGone(t *testing.T) {
	withFastPoll(t)
	var calls int32
	srv := httptest.NewServer(detailsHandler(func() string {
		if atomic.AddInt32(&calls, 1) >= 3 {
			return ""
		}
		return "deleting"
	}))
	defer srv.Close()

	r := newTestPostgresResource(t, srv)
	diags := r.waitForPostgresDeleted(context.Background(), context.Background(), "pjx", "aws-us-east-1", "tf-acc-pg", time.Minute, "test")
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if got := atomic.LoadInt32(&calls); got < 3 {
		t.Fatalf("expected at least 3 polls, got %d", got)
	}
}

func TestWaitForPostgresDeletedTimesOut(t *testing.T) {
	withFastPoll(t)
	srv := httptest.NewServer(detailsHandler(func() string { return "deleting" }))
	defer srv.Close()

	r := newTestPostgresResource(t, srv)
	opCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	diags := r.waitForPostgresDeleted(opCtx, context.Background(), "pjx", "aws-us-east-1", "tf-acc-pg", 20*time.Millisecond, "test")
	if !diags.HasError() {
		t.Fatal("expected a timeout error, got none")
	}
	if !strings.Contains(diags[0].Summary(), "Timeout") {
		t.Fatalf("expected timeout summary, got %q", diags[0].Summary())
	}
}

// A hung detail GET must surface as a Timeout within the budget: each GET is bound to a
// context derived from the timeout, not the provider-wide one (hours away, or never).
func TestWaitForPostgresRunningBoundsStuckGet(t *testing.T) {
	withFastPoll(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release // a GET that ignores the timeout blocks here until the test ends
	}))
	// Deferred in this order so close(release) unblocks the handler before srv.Close waits on it.
	defer srv.Close()
	defer close(release)

	r := newTestPostgresResource(t, srv)
	opCtx, cancelOp := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancelOp()
	summary := make(chan string, 1)
	go func() {
		_, diags := r.waitForPostgresRunning(opCtx, context.Background(), "pjx", "aws-us-east-1", "tf-acc-pg", 30*time.Millisecond, "test")
		if diags.HasError() {
			summary <- diags[0].Summary()
		} else {
			summary <- ""
		}
	}()
	select {
	case s := <-summary:
		if !strings.Contains(s, "Timeout") {
			t.Fatalf("expected a Timeout error from a stuck GET, got %q", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waitForPostgresRunning ignored its 30ms timeout on a stuck GET (it hung)")
	}
}

func TestWaitForPostgresDeletedBoundsStuckGet(t *testing.T) {
	withFastPoll(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	r := newTestPostgresResource(t, srv)
	opCtx, cancelOp := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancelOp()
	summary := make(chan string, 1)
	go func() {
		diags := r.waitForPostgresDeleted(opCtx, context.Background(), "pjx", "aws-us-east-1", "tf-acc-pg", 30*time.Millisecond, "test")
		if diags.HasError() {
			summary <- diags[0].Summary()
		} else {
			summary <- ""
		}
	}()
	select {
	case s := <-summary:
		if !strings.Contains(s, "Timeout") {
			t.Fatalf("expected a Timeout error from a stuck GET, got %q", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waitForPostgresDeleted ignored its 30ms timeout on a stuck GET (it hung)")
	}
}

func TestReadRemovesResourceOnNotFound(t *testing.T) {
	ctx := t.Context()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
	}))
	defer srv.Close()

	r := newTestPostgresResource(t, srv)
	resp := driveRead(t, ctx, r, nil)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Read on a 404 must not error (idiomatic drift handling), got: %+v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Fatal("Read on a 404 must RemoveResource (null state), but the resource is still present in state")
	}
}
