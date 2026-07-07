package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// runTimeoutAdoptCreate stalls the dispatch POST so the create deadline fires and the adopt
// path runs; the adopt GET answers a running row stamped created_at = adoptCreatedAt().
func runTimeoutAdoptCreate(t *testing.T, adoptCreatedAt func() time.Time) (*resource.CreateResponse, resource_postgres.PostgresModel) {
	t.Helper()
	ctx := context.Background()
	withFastPoll(t)
	withShortCreateTimeout(t, 100*time.Millisecond)
	withFastAdoptBudget(t, 2*time.Second)

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			<-release // the dispatch POST never responds, so the create deadline fires
			return
		}
		body := pgBodyForState(postgresStateRunning)
		body.Name = "tf-acc-pg"
		body.CreatedAt = adoptCreatedAt()
		writePGJSON(w, http.StatusOK, body)
	}))
	defer srv.Close()
	defer close(release)

	r := newTestPostgresResource(t, srv)
	schema := resource_postgres.PostgresResourceSchema(ctx)
	planRaw := mkPGRaw(t, ctx, map[string]tftypes.Value{
		"size":         strRaw("m8gd.large"),
		"storage_size": numRaw(64),
	})
	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: planRaw}}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schema, Raw: pgNullStateRaw(t, ctx)}}

	done := make(chan struct{})
	go func() {
		r.Create(ctx, req, resp)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Create ignored its create timeout on a stuck POST (it hung)")
	}

	return resp, pgReadCreateState(t, resp)
}

// pgReadCreateState decodes a persisted create state; a null (no-persist) state stays the zero
// model, so callers assert on resp.State.Raw.IsNull() rather than reading a null root back.
func pgReadCreateState(t *testing.T, resp *resource.CreateResponse) resource_postgres.PostgresModel {
	t.Helper()
	var out resource_postgres.PostgresModel
	if resp.State.Raw.IsNull() {
		return out
	}
	if diags := resp.State.Get(context.Background(), &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	return out
}

// A row born at or after dispatch is this create's (committed but timed out): adopt it so the
// next apply replaces it instead of orphaning into a name conflict; the timeout diag still taints.
func TestCreateTimeoutAdoptsOwnFreshRow(t *testing.T) {
	sampleID := sampleDetailedPostgresResponse().Id
	resp, out := runTimeoutAdoptCreate(t, func() time.Time { return time.Now() })

	if !resp.Diagnostics.HasError() {
		t.Fatal("a timed-out create must still surface the timeout error")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(got, "Timeout while creating") {
		t.Fatalf("summary = %q, want a create-timeout classification", got)
	}
	if out.Id.ValueString() != sampleID {
		t.Errorf("adopted state id = %q, want the created row %q", out.Id.ValueString(), sampleID)
	}
}

// A lost or delayed name-conflict 500 can surface as this timeout, so a row predating the
// dispatch may be another owner's database: adopting it would let a later apply destroy it.
func TestCreateTimeoutDoesNotAdoptForeignOlderRow(t *testing.T) {
	resp, _ := runTimeoutAdoptCreate(t, func() time.Time { return time.Now().Add(-time.Hour) })

	if !resp.Diagnostics.HasError() {
		t.Fatal("a timed-out create must still surface the timeout error")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(got, "Timeout while creating") {
		t.Fatalf("summary = %q, want a create-timeout classification", got)
	}
	if !resp.State.Raw.IsNull() {
		t.Errorf("a pre-existing foreign row must not be adopted, but state was persisted")
	}
}

// runCancelAdoptCreate cancels the parent context while the dispatch POST is in flight, so the
// create classifies as a parent cancellation (a graceful stop). The adopt GET answers a running
// row stamped created_at = adoptCreatedAt(); it exercises the WithoutCancel salvage lookup.
func runCancelAdoptCreate(t *testing.T, adoptCreatedAt func() time.Time) (*resource.CreateResponse, resource_postgres.PostgresModel) {
	t.Helper()
	ctx, cancelCreate := context.WithCancel(context.Background())
	withFastPoll(t)
	withFastAdoptBudget(t, 2*time.Second)
	withShortCreateTimeout(t, 30*time.Second) // the cancel, not the deadline, must end the POST

	var once sync.Once
	posted := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			once.Do(func() { close(posted) })
			<-release // hold the POST in flight; the parent cancel aborts the client below
			return
		}
		body := pgBodyForState(postgresStateRunning)
		body.Name = "tf-acc-pg"
		body.CreatedAt = adoptCreatedAt()
		writePGJSON(w, http.StatusOK, body)
	}))
	defer srv.Close()
	defer close(release)

	r := newTestPostgresResource(t, srv)
	schema := resource_postgres.PostgresResourceSchema(ctx)
	planRaw := mkPGRaw(t, ctx, map[string]tftypes.Value{
		"size":         strRaw("m8gd.large"),
		"storage_size": numRaw(64),
	})
	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: planRaw}}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schema, Raw: pgNullStateRaw(t, ctx)}}

	go func() {
		<-posted
		cancelCreate() // cancel the parent while the dispatch POST is in flight
	}()
	done := make(chan struct{})
	go func() {
		r.Create(ctx, req, resp)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Create hung after a parent cancel raced a committed dispatch")
	}

	return resp, pgReadCreateState(t, resp)
}

// A parent cancel (a graceful stop) can race a committed create; a WithoutCancel lookup adopts
// this create's own fresh row so the next apply replaces it instead of orphaning into a conflict.
func TestCreateCancelAdoptsOwnFreshRow(t *testing.T) {
	sampleID := sampleDetailedPostgresResponse().Id
	resp, out := runCancelAdoptCreate(t, func() time.Time { return time.Now() })

	if !resp.Diagnostics.HasError() {
		t.Fatal("a cancelled create must still surface the cancellation error")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(got, "Cancelled while creating") {
		t.Fatalf("summary = %q, want a cancellation classification", got)
	}
	if out.Id.ValueString() != sampleID {
		t.Errorf("adopted state id = %q, want the created row %q", out.Id.ValueString(), sampleID)
	}
}

// The created_at gate still guards the cancel path: a row predating dispatch is a foreign
// database and must not be adopted.
func TestCreateCancelDoesNotAdoptForeignOlderRow(t *testing.T) {
	resp, _ := runCancelAdoptCreate(t, func() time.Time { return time.Now().Add(-time.Hour) })

	if !resp.Diagnostics.HasError() {
		t.Fatal("a cancelled create must still surface the cancellation error")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(got, "Cancelled while creating") {
		t.Fatalf("summary = %q, want a cancellation classification", got)
	}
	if !resp.State.Raw.IsNull() {
		t.Errorf("a pre-existing foreign row must not be adopted, but state was persisted")
	}
}
