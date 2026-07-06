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

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// pgBodyForState: a creating snapshot carries nil hostname/connection_string, a running
// one populates them, modeling the readiness transition the create wait observes.
func pgBodyForState(state string) ubicloud_client.PostgresDatabase {
	body := sampleDetailedPostgresResponse()
	body.State = state
	if state == postgresStateRunning {
		body.Hostname = ptrTo("tf-acc-pg.example.com")
		body.ConnectionString = ptrTo("postgres://postgres:supersecret@tf-acc-pg.example.com:5432/postgres")
	} else {
		body.Hostname = nil
		body.ConnectionString = nil
	}
	return body
}

// writePGJSON sets a json Content-Type; the generated client parses bodies only then.
func writePGJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// withShortDeleteTimeout lowers the package delete-timeout default: the timeouts custom
// type exposes no settable attributes through the generated test schema.
func withShortDeleteTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := postgresDeleteTimeoutDefault
	postgresDeleteTimeoutDefault = d
	t.Cleanup(func() { postgresDeleteTimeoutDefault = prev })
}

func driveDelete(t *testing.T, ctx context.Context, r *postgresResource, stateOver map[string]tftypes.Value) *resource.DeleteResponse {
	t.Helper()
	pgSchema := resource_postgres.PostgresResourceSchema(ctx)
	stateRaw := mkPGRaw(t, ctx, stateOver)
	req := resource.DeleteRequest{State: tfsdk.State{Schema: pgSchema, Raw: stateRaw}}
	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: pgSchema, Raw: stateRaw}}
	r.Delete(ctx, req, resp)
	return resp
}

func TestCreateBlocksUntilRunningAndRemapsRunningSurface(t *testing.T) {
	withFastPoll(t)
	ctx := context.Background()
	var detailGets int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			writePGJSON(w, http.StatusOK, pgBodyForState("creating"))
		case strings.HasSuffix(r.URL.Path, "/config"):
			writePGJSON(w, http.StatusOK, ubicloud_client.PostgresConfig{PgConfig: map[string]string{}, PgbouncerConfig: map[string]string{}})
		default: // the readiness detail GET
			if atomic.AddInt32(&detailGets, 1) >= 2 {
				writePGJSON(w, http.StatusOK, pgBodyForState(postgresStateRunning))
				return
			}
			writePGJSON(w, http.StatusOK, pgBodyForState("creating"))
		}
	}))
	defer srv.Close()

	r := newTestPostgresResource(t, srv)
	resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
		"size":         strRaw("m8gd.large"),
		"storage_size": numRaw(64),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}

	var model resource_postgres.PostgresModel
	if d := resp.State.Get(ctx, &model); d.HasError() {
		t.Fatalf("reading persisted state: %+v", d)
	}
	if got := model.State.ValueString(); got != postgresStateRunning {
		t.Errorf("final state = %q, want running (the running re-map must overwrite the creating snapshot)", got)
	}
	if got := model.Hostname.ValueString(); got != "tf-acc-pg.example.com" {
		t.Errorf("hostname = %q, want the running surface (re-map of the running GET)", got)
	}
	if got := model.ConnectionString.ValueString(); !strings.Contains(got, "tf-acc-pg.example.com") {
		t.Errorf("connection_string = %q, want the populated running value", got)
	}
	if got := atomic.LoadInt32(&detailGets); got < 2 {
		t.Errorf("expected the wait to poll the detail GET at least twice (creating then running), got %d", got)
	}
}

// A dispatch that succeeded but never reached running must taint, not orphan: persist the
// creating snapshot so the next apply replaces it instead of recreating into a name conflict.
func TestCreateTaintsOnReadinessTimeout(t *testing.T) {
	withFastPoll(t)
	withShortCreateTimeout(t, 60*time.Millisecond)
	ctx := context.Background()
	srv := httptest.NewServer(detailsHandler(func() string { return "creating" }))
	defer srv.Close()

	r := newTestPostgresResource(t, srv)
	resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
		"size":             strRaw("m8gd.large"),
		"storage_size":     numRaw(64),
		"pg_config":        rawUnknown(t, ctx, "pg_config"),
		"pgbouncer_config": rawUnknown(t, ctx, "pgbouncer_config"),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected a readiness timeout, got success")
	}
	summary := resp.Diagnostics.Errors()[0].Summary()
	if !strings.Contains(summary, "Timeout") || !strings.Contains(summary, "become ready") {
		t.Fatalf("summary = %q, want the readiness-timeout classification (tainting, not a name conflict)", summary)
	}

	var model resource_postgres.PostgresModel
	// Get does not error on unknown values; unknown-ness is asserted below.
	resp.State.Get(ctx, &model)
	if got := model.Id.ValueString(); got == "" {
		t.Error("the creating snapshot must be persisted (id set), so the timed-out create is tracked")
	}
	if got := model.State.ValueString(); got != "creating" {
		t.Errorf("persisted state = %q, want the creating snapshot", got)
	}
	if model.PgConfig.IsUnknown() {
		t.Error("ensurePostgresConfigKnown must make pg_config concrete in the persisted partial state")
	}
	if model.PgbouncerConfig.IsUnknown() {
		t.Error("ensurePostgresConfigKnown must make pgbouncer_config concrete in the persisted partial state")
	}
}

func TestEnsurePostgresConfigKnown(t *testing.T) {
	set := types.MapValueMust(types.StringType, map[string]attr.Value{"max_connections": types.StringValue("200")})
	t.Run("unknown becomes empty known", func(t *testing.T) {
		m := &resource_postgres.PostgresModel{PgConfig: types.MapUnknown(types.StringType), PgbouncerConfig: types.MapUnknown(types.StringType)}
		ensurePostgresConfigKnown(m)
		if m.PgConfig.IsUnknown() || len(m.PgConfig.Elements()) != 0 {
			t.Errorf("pg_config = %v, want empty known map", m.PgConfig)
		}
		if m.PgbouncerConfig.IsUnknown() || len(m.PgbouncerConfig.Elements()) != 0 {
			t.Errorf("pgbouncer_config = %v, want empty known map", m.PgbouncerConfig)
		}
	})
	t.Run("known values are untouched", func(t *testing.T) {
		m := &resource_postgres.PostgresModel{PgConfig: set, PgbouncerConfig: types.MapNull(types.StringType)}
		ensurePostgresConfigKnown(m)
		if m.PgConfig.Equal(set) != true {
			t.Errorf("pg_config = %v, want the original set value untouched", m.PgConfig)
		}
		if !m.PgbouncerConfig.IsNull() {
			t.Errorf("pgbouncer_config = %v, want the null value untouched (only unknown is defaulted)", m.PgbouncerConfig)
		}
	})
}

// A 200 whose body is not the detail JSON leaves JSON200 nil with no transport error;
// the wait must keep polling instead of dereferencing nil.
func TestWaitForPostgresRunningSkipsUnparseableBody(t *testing.T) {
	withFastPoll(t)
	var gets int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&gets, 1) == 1 {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not a json body"))
			return
		}
		writePGJSON(w, http.StatusOK, pgBodyForState(postgresStateRunning))
	}))
	defer srv.Close()

	r := newTestPostgresResource(t, srv)
	pg, diags := r.waitForPostgresRunning(context.Background(), context.Background(), "pjx", "aws-us-east-1", "tf-acc-pg", time.Minute, "test")
	if diags.HasError() {
		t.Fatalf("a 200 with an unparseable body must re-poll, not error: %+v", diags)
	}
	if pg == nil || pg.State != postgresStateRunning {
		t.Fatalf("expected the later running snapshot, got %+v", pg)
	}
	if got := atomic.LoadInt32(&gets); got < 2 {
		t.Fatalf("expected a re-poll after the unparseable body, got %d GETs", got)
	}
}

// A terminal non-200 (a 403: access revoked mid-wait) must error immediately with the status
// and body, never loop: only 5xx and 429 are the budgeted transient class.
func TestWaitForPostgresRunningUnexpectedStatusReturnsImmediately(t *testing.T) {
	withFastPoll(t)
	var gets int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&gets, 1)
		writePGJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{"message": "boom-running-detail"}})
	}))
	defer srv.Close()

	r := newTestPostgresResource(t, srv)
	_, diags := r.waitForPostgresRunning(context.Background(), context.Background(), "pjx", "aws-us-east-1", "tf-acc-pg", time.Minute, "test")
	if !diags.HasError() {
		t.Fatal("a terminal non-200 readiness GET must error")
	}
	if got := diags[0].Summary(); got != "Unexpected HTTP status code waiting for postgres database to become ready" {
		t.Fatalf("summary = %q, want the unexpected-status classification", got)
	}
	if got := diags[0].Detail(); !strings.Contains(got, "403") || !strings.Contains(got, "boom-running-detail") {
		t.Fatalf("detail = %q, want the status and body included", got)
	}
	if got := atomic.LoadInt32(&gets); got != 1 {
		t.Fatalf("a terminal status must return after exactly one GET (no looping), got %d", got)
	}
}

// The DELETE only accepts the async teardown; Delete must poll until the row is gone (GET 404).
func TestDeleteWaitsUntilGone(t *testing.T) {
	withFastPoll(t)
	ctx := context.Background()
	var gets int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if atomic.AddInt32(&gets, 1) >= 2 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
			return
		}
		writePGJSON(w, http.StatusOK, pgBodyForState("deleting"))
	}))
	defer srv.Close()

	r := newTestPostgresResource(t, srv)
	resp := driveDelete(t, ctx, r, nil)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	if got := atomic.LoadInt32(&gets); got < 2 {
		t.Fatalf("Delete must poll the detail GET until the row is gone, got %d GETs", got)
	}
}

// A missing database itself answers 204; a 404 DELETE means the project or location went
// away out of band, so the delete converges with no teardown poll.
func TestDeleteShortCircuitsOn404(t *testing.T) {
	ctx := context.Background()
	var gets int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			atomic.AddInt32(&gets, 1)
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found","type":"NotFound"}}`))
	}))
	defer srv.Close()

	r := newTestPostgresResource(t, srv)
	resp := driveDelete(t, ctx, r, nil)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a 404 DELETE must short-circuit cleanly: %+v", resp.Diagnostics)
	}
	if got := atomic.LoadInt32(&gets); got != 0 {
		t.Fatalf("a 404 DELETE must not poll for teardown, got %d GETs", got)
	}
}

func TestDeleteUnexpectedStatus(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writePGJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"code": 500, "message": "boom-delete", "type": "InternalError"}})
	}))
	defer srv.Close()

	r := newTestPostgresResource(t, srv)
	resp := driveDelete(t, ctx, r, nil)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a 500 DELETE must error")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); got != "Unexpected HTTP status code deleting postgres database" {
		t.Fatalf("summary = %q, want the unexpected-status classification", got)
	}
	if got := resp.Diagnostics.Errors()[0].Detail(); !strings.Contains(got, "boom-delete") {
		t.Fatalf("detail = %q, want the response body included", got)
	}
}

func TestDeleteBoundsStuckDeleteByDeleteTimeout(t *testing.T) {
	withFastPoll(t)
	withShortDeleteTimeout(t, 100*time.Millisecond)
	ctx := context.Background()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release // the DELETE never responds
	}))
	defer srv.Close()
	defer close(release)

	r := newTestPostgresResource(t, srv)
	pgSchema := resource_postgres.PostgresResourceSchema(ctx)
	stateRaw := mkPGRaw(t, ctx, nil)
	req := resource.DeleteRequest{State: tfsdk.State{Schema: pgSchema, Raw: stateRaw}}
	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: pgSchema, Raw: stateRaw}}

	done := make(chan struct{})
	go func() {
		r.Delete(ctx, req, resp)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Delete ignored its delete timeout on a stuck DELETE (it hung)")
	}
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected a bounded delete error from a stuck DELETE, got success")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(got, "Timeout while deleting") {
		t.Fatalf("summary = %q, want a delete-timeout classification", got)
	}
}

// Terminal-class analogue for the delete wait: a non-404 non-200 errors immediately (404 is success).
func TestWaitForPostgresDeletedUnexpectedStatus(t *testing.T) {
	withFastPoll(t)
	var gets int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&gets, 1)
		writePGJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{"message": "boom-deleted"}})
	}))
	defer srv.Close()

	r := newTestPostgresResource(t, srv)
	diags := r.waitForPostgresDeleted(context.Background(), context.Background(), "pjx", "aws-us-east-1", "tf-acc-pg", time.Minute, "test")
	if !diags.HasError() {
		t.Fatal("a terminal non-404 non-200 GET while waiting for deletion must error")
	}
	if got := diags[0].Summary(); got != "Unexpected HTTP status code waiting for postgres database to be deleted" {
		t.Fatalf("summary = %q, want the unexpected-status classification", got)
	}
	if got := diags[0].Detail(); !strings.Contains(got, "403") || !strings.Contains(got, "boom-deleted") {
		t.Fatalf("detail = %q, want the status and body included", got)
	}
	if got := atomic.LoadInt32(&gets); got != 1 {
		t.Fatalf("a terminal status must return after exactly one GET (no looping), got %d", got)
	}
}

func TestWaitForPostgresDeletedTimeoutDetailNamesDeleteKnob(t *testing.T) {
	withFastPoll(t)
	srv := httptest.NewServer(detailsHandler(func() string { return "deleting" }))
	defer srv.Close()

	r := newTestPostgresResource(t, srv)
	opCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	diags := r.waitForPostgresDeleted(opCtx, context.Background(), "pjx", "aws-us-east-1", "tf-acc-pg", 20*time.Millisecond, "test")
	if !diags.HasError() {
		t.Fatal("expected a delete timeout, got none")
	}
	if got := diags[0].Detail(); !strings.Contains(got, "timeouts.delete") {
		t.Fatalf("detail = %q, want it to name timeouts.delete", got)
	}
}

func TestPostgresSchemaTimeoutsBlockCreateDeleteNotUpdate(t *testing.T) {
	ctx := context.Background()
	r := &postgresResource{}
	resp := &resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, resp)

	block, ok := resp.Schema.Blocks["timeouts"]
	if !ok {
		t.Fatal("schema has no timeouts block")
	}
	snb, ok := block.(schema.SingleNestedBlock)
	if !ok {
		t.Fatalf("timeouts block is %T, want schema.SingleNestedBlock", block)
	}
	if _, ok := snb.Attributes["create"]; !ok {
		t.Error("timeouts block must expose create")
	}
	if _, ok := snb.Attributes["delete"]; !ok {
		t.Error("timeouts block must expose delete")
	}
	if _, ok := snb.Attributes["update"]; ok {
		t.Error("timeouts block must NOT expose update (the Update convergence wait is a follow-up)")
	}
	if _, ok := snb.CustomType.(timeouts.Type); !ok {
		t.Errorf("timeouts block CustomType is %T, want timeouts.Type", snb.CustomType)
	}
	// The canonical helper attaches a duration validator; the generated block does not.
	create, ok := snb.Attributes["create"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("create attribute is %T, want schema.StringAttribute", snb.Attributes["create"])
	}
	if len(create.Validators) == 0 {
		t.Error("the canonical timeouts block must attach a duration validator to create")
	}
}
