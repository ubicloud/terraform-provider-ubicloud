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

func rawNull(t *testing.T, ctx context.Context, name string) tftypes.Value {
	t.Helper()
	objType := postgresResourceSchemaObjType(t, ctx)
	typ, ok := objType.AttributeTypes[name]
	if !ok {
		t.Fatalf("schema has no attribute %q", name)
	}
	return tftypes.NewValue(typ, nil)
}

func rawUnknown(t *testing.T, ctx context.Context, name string) tftypes.Value {
	t.Helper()
	objType := postgresResourceSchemaObjType(t, ctx)
	typ, ok := objType.AttributeTypes[name]
	if !ok {
		t.Fatalf("schema has no attribute %q", name)
	}
	return tftypes.NewValue(typ, tftypes.UnknownValue)
}

func createPostBody(t *testing.T, capRT *captureRT) map[string]any {
	t.Helper()
	for i := range capRT.reqs {
		rq := capRT.reqs[i]
		if rq.Method == http.MethodPost && strings.HasSuffix(rq.Path, "/postgres/tf-acc-pg") {
			return bodyJSON(t, rq.Body)
		}
	}
	t.Fatalf("no create POST .../postgres/tf-acc-pg; calls=%+v", capRT.reqs)
	return nil
}

func assertNoPost(t *testing.T, capRT *captureRT) {
	t.Helper()
	for _, rq := range capRT.reqs {
		if rq.Method == http.MethodPost {
			t.Errorf("no POST must be issued; got %s %s", rq.Method, rq.Path)
		}
	}
}

func TestCreateDispatchesRestoreOnRestoreTarget(t *testing.T) {
	ctx := context.Background()
	r, capRT := newCapturingPostgresResource(t)
	capRT.detailState = "running"
	resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
		"restore_target": strRaw("2026-06-24T11:00:00Z"),
		"parent":         strRaw("tf-acc-src"),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	d := capRT.dispatchReqs()
	if len(d) != 1 || d[0].Method != http.MethodPost || !strings.HasSuffix(d[0].Path, "/postgres/tf-acc-src/restore") {
		t.Fatalf("dispatch = %+v, want one POST .../postgres/tf-acc-src/restore", d)
	}
}

func TestCreateDispatchesReadReplicaOnParent(t *testing.T) {
	ctx := context.Background()
	r, capRT := newCapturingPostgresResource(t)
	capRT.detailState = "running"
	resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
		"parent": strRaw("tf-acc-src"),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	d := capRT.dispatchReqs()
	if len(d) != 1 || d[0].Method != http.MethodPost || !strings.HasSuffix(d[0].Path, "/postgres/tf-acc-src/read-replica") {
		t.Fatalf("dispatch = %+v, want one POST .../postgres/tf-acc-src/read-replica", d)
	}
}

// A restore also names parent as its source, so restore must win when both are set.
func TestCreateDispatchPrefersRestoreOverParent(t *testing.T) {
	ctx := context.Background()
	r, capRT := newCapturingPostgresResource(t)
	capRT.detailState = "running"
	resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
		"restore_target": strRaw("2026-06-24T11:00:00Z"),
		"parent":         strRaw("tf-acc-src"),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	d := capRT.dispatchReqs()
	if len(d) != 1 || !strings.HasSuffix(d[0].Path, "/postgres/tf-acc-src/restore") {
		t.Fatalf("dispatch = %+v, want restore to win over the read-replica path", d)
	}
	if strings.HasSuffix(d[0].Path, "/read-replica") {
		t.Errorf("a plan with both restore_target and parent must NOT take the read-replica path: %s", d[0].Path)
	}
}

// runStuckPostCreate drives r.Create against a POST that never responds and returns the POST
// path reached plus the response; only r.Create runs in the goroutine (mkPGRaw may t.Fatal).
func runStuckPostCreate(t *testing.T, planOver map[string]tftypes.Value) (string, *resource.CreateResponse) {
	t.Helper()
	ctx := context.Background()
	withFastPoll(t)
	withShortCreateTimeout(t, 100*time.Millisecond)
	withFastAdoptBudget(t, 200*time.Millisecond)

	var mu sync.Mutex
	var postPath string
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mu.Lock()
			postPath = r.URL.Path
			mu.Unlock()
			<-release // the dispatch POST never responds
			return
		}
		// The post-timeout adopt lookup GET: 404 everywhere, so the lookup finds nothing.
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
	}))
	defer srv.Close()
	defer close(release)

	r := newTestPostgresResource(t, srv)
	schema := resource_postgres.PostgresResourceSchema(ctx)
	planRaw := mkPGRaw(t, ctx, planOver)
	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: planRaw}}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schema, Raw: pgNullStateRaw(t, ctx)}}

	done := make(chan struct{})
	go func() {
		r.Create(ctx, req, resp)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Create ignored its create timeout on a stuck POST (it hung)")
	}
	mu.Lock()
	defer mu.Unlock()
	return postPath, resp
}

func TestCreateBoundsStuckReadReplicaPostByCreateTimeout(t *testing.T) {
	postPath, resp := runStuckPostCreate(t, map[string]tftypes.Value{
		"parent": strRaw("tf-acc-src"),
	})
	if !strings.HasSuffix(postPath, "/postgres/tf-acc-src/read-replica") {
		t.Fatalf("stuck POST path = %q, want the read-replica endpoint", postPath)
	}
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected a bounded create error from a stuck read-replica POST, got success")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(got, "Timeout while creating") {
		t.Fatalf("summary = %q, want a create-timeout classification", got)
	}
	if !resp.State.Raw.IsNull() {
		t.Errorf("a stuck read-replica POST the server never accepted must persist no state")
	}
}

func TestCreateBoundsStuckRestorePostByCreateTimeout(t *testing.T) {
	postPath, resp := runStuckPostCreate(t, map[string]tftypes.Value{
		"restore_target": strRaw("2026-06-24T11:00:00Z"),
		"parent":         strRaw("tf-acc-src"),
	})
	if !strings.HasSuffix(postPath, "/postgres/tf-acc-src/restore") {
		t.Fatalf("stuck POST path = %q, want the restore endpoint", postPath)
	}
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected a bounded create error from a stuck restore POST, got success")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(got, "Timeout while creating") {
		t.Fatalf("summary = %q, want a create-timeout classification", got)
	}
	if !resp.State.Raw.IsNull() {
		t.Errorf("a stuck restore POST the server never accepted must persist no state")
	}
}

func TestCreateClassifiesParentCancelAsCancelled(t *testing.T) {
	withFastPoll(t)
	withFastAdoptBudget(t, 50*time.Millisecond)
	// A pre-cancelled parent never sends the POST, so nothing of ours committed; the fresh row this
	// server would answer is a foreign database, and the salvage must be skipped, not adopt it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := pgBodyForState(postgresStateRunning)
		body.Name = "tf-acc-pg"
		body.CreatedAt = time.Now()
		writePGJSON(w, http.StatusOK, body)
	}))
	defer srv.Close()

	// Build the plan on a live context; only r.Create runs under the cancelled one.
	buildCtx := context.Background()
	r := newTestPostgresResource(t, srv)
	schema := resource_postgres.PostgresResourceSchema(buildCtx)
	planRaw := mkPGRaw(t, buildCtx, map[string]tftypes.Value{
		"size":         strRaw("m8gd.large"),
		"storage_size": numRaw(64),
	})
	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: planRaw}}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schema, Raw: pgNullStateRaw(t, buildCtx)}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: the dispatch must fail and classify as cancellation
	r.Create(ctx, req, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected a cancellation error from a pre-cancelled parent context, got success")
	}
	got := resp.Diagnostics.Errors()[0].Summary()
	if !strings.Contains(got, "Cancelled") {
		t.Fatalf("summary = %q, want a cancellation classification", got)
	}
	if strings.Contains(got, "Timeout") {
		t.Fatalf("summary = %q, must NOT be a timeout classification (raising timeouts.create cannot help a cancelled run)", got)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("a pre-cancelled create never sent its POST; a fresh foreign row must not be adopted")
	}
}

func TestCreatePrimaryOmitsUnsetBodyFields(t *testing.T) {
	ctx := context.Background()
	r, capRT := newCapturingPostgresResource(t)
	capRT.detailState = "running"
	resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
		"size":         strRaw("m8gd.large"),
		"storage_size": numRaw(64),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	b := createPostBody(t, capRT)
	for _, k := range []string{"flavor", "restrict_by_default", "private_subnet_name", "pg_config", "pgbouncer_config", "tags"} {
		if _, ok := b[k]; ok {
			t.Errorf("create body must omit %q when unset (null), got %v", k, b[k])
		}
	}
}

func TestCreatePrimaryOmitsUnknownBodyFields(t *testing.T) {
	ctx := context.Background()
	r, capRT := newCapturingPostgresResource(t)
	capRT.detailState = "running"
	resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
		"size":                strRaw("m8gd.large"),
		"storage_size":        numRaw(64),
		"flavor":              rawUnknown(t, ctx, "flavor"),
		"restrict_by_default": rawUnknown(t, ctx, "restrict_by_default"),
		"private_subnet_name": rawUnknown(t, ctx, "private_subnet_name"),
		"pg_config":           rawUnknown(t, ctx, "pg_config"),
		"pgbouncer_config":    rawUnknown(t, ctx, "pgbouncer_config"),
		"tags":                rawUnknown(t, ctx, "tags"),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	b := createPostBody(t, capRT)
	for _, k := range []string{"flavor", "restrict_by_default", "private_subnet_name", "pg_config", "pgbouncer_config", "tags"} {
		if _, ok := b[k]; ok {
			t.Errorf("create body must omit %q when unknown, got %v", k, b[k])
		}
	}
}

func TestCreatePrimaryOmitsEmptyStringBodyFields(t *testing.T) {
	ctx := context.Background()
	r, capRT := newCapturingPostgresResource(t)
	capRT.detailState = "running"
	resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
		"size":                strRaw("m8gd.large"),
		"storage_size":        numRaw(64),
		"flavor":              strRaw(""),
		"private_subnet_name": strRaw(""),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	b := createPostBody(t, capRT)
	for _, k := range []string{"flavor", "private_subnet_name"} {
		if _, ok := b[k]; ok {
			t.Errorf("create body must omit %q when empty, got %v", k, b[k])
		}
	}
}

// The plan-time size guard defers an unknown value; an interpolation that resolves
// empty reaches apply, so Create re-checks and must issue no POST.
func TestCreatePrimaryRejectsMissingSize(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		size tftypes.Value
	}{
		{"empty", strRaw("")},
		{"whitespace", strRaw("   ")},
		{"null", rawNull(t, ctx, "size")},
		{"unknown", rawUnknown(t, ctx, "size")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, capRT := newCapturingPostgresResource(t)
			capRT.detailState = "running"
			resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
				"size":         c.size,
				"storage_size": numRaw(64),
			})
			if !resp.Diagnostics.HasError() {
				t.Fatal("expected an error for a primary create with no size, got success")
			}
			if got := resp.Diagnostics.Errors()[0].Summary(); got != "Missing size for primary postgres database" {
				t.Fatalf("summary = %q, want Missing size for primary postgres database", got)
			}
			assertNoPost(t, capRT)
		})
	}
}

// storage_size is a non-omitempty required int in the generated create body: unguarded,
// null/unknown collapse to a POSTed 0, which the backend rejects opaquely (pos_int maps 0 to nil).
func TestCreatePrimaryRejectsMissingStorageSize(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name        string
		storageSize tftypes.Value
	}{
		{"null", rawNull(t, ctx, "storage_size")},
		{"unknown", rawUnknown(t, ctx, "storage_size")},
		{"zero", numRaw(0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, capRT := newCapturingPostgresResource(t)
			capRT.detailState = "running" // keep the test bounded if the guard ever regresses
			resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
				"size":         strRaw("m8gd.large"),
				"storage_size": c.storageSize,
			})
			if !resp.Diagnostics.HasError() {
				t.Fatal("expected an error for a primary create with no storage_size, got success")
			}
			if got := resp.Diagnostics.Errors()[0].Summary(); got != "Missing storage_size for primary postgres database" {
				t.Fatalf("summary = %q, want Missing storage_size for primary postgres database", got)
			}
			assertNoPost(t, capRT)
		})
	}
}

// A non-JSON 200 (JSON200 nil) whose adopt lookup finds nothing is the genuinely-absent
// case: error clearly, persist no state, and let a clean recreate follow.
func TestCreateFailsClosedOnEmptyBody(t *testing.T) {
	withFastPoll(t)
	withFastAdoptBudget(t, 50*time.Millisecond)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found","type":"NotFound"}}`))
	}))
	defer srv.Close()

	r := newTestPostgresResource(t, srv)
	resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
		"size":         strRaw("m8gd.large"),
		"storage_size": numRaw(64),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected a fail-closed error on a 200 with no database body, got success")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); got != "Empty response creating postgres database" {
		t.Fatalf("summary = %q, want Empty response creating postgres database", got)
	}
	if !resp.State.Raw.IsNull() {
		t.Errorf("a genuinely-absent create must persist no state")
	}
}

// Adopt only on a non-JSON 200 (a proxy mangled a maybe-committed create); a definitive HTTP
// error never adopts, 5xx included: the backend answers a name conflict with a generic 500,
// so adopting on one could claim a database this resource did not create.
func TestCreateAdoptsOnNonJSON200NotOnHTTPError(t *testing.T) {
	sampleID := sampleDetailedPostgresResponse().Id
	cases := []struct {
		name        string
		postStatus  int  // status the create POST returns; ignored when postNonJSON
		postNonJSON bool // POST answers 200 with a text/html body (JSON200 nil)
		found       bool // whether the follow-up adopt GET returns the created row
		foreignOld  bool // when found, stamp created_at before dispatch so the row reads as foreign
		wantSummary string
		wantAdopt   bool
	}{
		{"non-JSON 200 then found adopts", 0, true, true, false, "Empty response creating postgres database", true},
		{"non-JSON 200 then found older foreign not adopted", 0, true, true, true, "Empty response creating postgres database", false},
		{"non-JSON 200 then absent no adopt", 0, true, false, false, "Empty response creating postgres database", false},
		{"5xx with existing row not adopted", http.StatusInternalServerError, false, true, false, "Unexpected HTTP status code creating postgres database", false},
		{"4xx with existing row not adopted", http.StatusConflict, false, true, false, "Unexpected HTTP status code creating postgres database", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withFastPoll(t)
			withFastAdoptBudget(t, 50*time.Millisecond)
			ctx := context.Background()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					if c.postNonJSON {
						w.Header().Set("Content-Type", "text/plain")
						w.WriteHeader(http.StatusOK)
						return
					}
					w.WriteHeader(c.postStatus)
					_, _ = w.Write([]byte(`{"error":{"code":500,"message":"boom","type":"InternalError"}}`))
					return
				}
				// The adopt lookup GET: the created row when found, else 404.
				if !c.found {
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found","type":"NotFound"}}`))
					return
				}
				body := pgBodyForState(postgresStateRunning)
				body.Name = "tf-acc-pg"
				// created_at at/after dispatch reads as this create's row; an hour-old
				// stamp predates the skew allowance and reads as foreign.
				if c.foreignOld {
					body.CreatedAt = time.Now().Add(-time.Hour)
				} else {
					body.CreatedAt = time.Now()
				}
				writePGJSON(w, http.StatusOK, body)
			}))
			defer srv.Close()

			r := newTestPostgresResource(t, srv)
			resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
				"size":         strRaw("m8gd.large"),
				"storage_size": numRaw(64),
			})
			if !resp.Diagnostics.HasError() {
				t.Fatal("an ambiguous/failed dispatch must still surface an error")
			}
			if got := resp.Diagnostics.Errors()[0].Summary(); got != c.wantSummary {
				t.Fatalf("summary = %q, want %q", got, c.wantSummary)
			}
			if c.wantAdopt {
				var out resource_postgres.PostgresModel
				if diags := resp.State.Get(ctx, &out); diags.HasError() {
					t.Fatalf("state get: %+v", diags)
				}
				if out.Id.ValueString() != sampleID {
					t.Errorf("adopted state id = %q, want the created row %q", out.Id.ValueString(), sampleID)
				}
			} else if !resp.State.Raw.IsNull() {
				t.Errorf("no adoption expected, but state was persisted")
			}
		})
	}
}

func TestCreateReadReplicaTrimsParentPath(t *testing.T) {
	ctx := context.Background()
	r, capRT := newCapturingPostgresResource(t)
	capRT.detailState = "running"
	resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
		"parent": strRaw("  tf-acc-src  "),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	d := capRT.dispatchReqs()
	if len(d) != 1 || d[0].Method != http.MethodPost || !strings.HasSuffix(d[0].Path, "/postgres/tf-acc-src/read-replica") {
		t.Fatalf("dispatch = %+v, want one POST .../postgres/tf-acc-src/read-replica (parent trimmed)", d)
	}
}

// A 5xx read-replica create is never adopted (name conflicts surface as 500), so no lookup runs.
func TestCreateReadReplicaUnexpectedStatus(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"code":500,"message":"boom","type":"InternalError"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found","type":"NotFound"}}`))
	}))
	defer srv.Close()

	r := newTestPostgresResource(t, srv)
	resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
		"parent": strRaw("tf-acc-src"),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error on a non-200 read-replica create, got success")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); got != "Unexpected HTTP status code creating postgres read replica" {
		t.Fatalf("summary = %q, want Unexpected HTTP status code creating postgres read replica", got)
	}
}

// A blank restore source must error before ever addressing POST .../postgres//restore.
func TestCreateRestoreRejectsBlankParent(t *testing.T) {
	ctx := context.Background()
	r, capRT := newCapturingPostgresResource(t)
	capRT.detailState = "running" // keep the test bounded if the guard ever regresses
	resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
		"restore_target": strRaw("2026-06-24T11:00:00Z"),
		"parent":         strRaw("   "), // blank source
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error for a restore with a blank source, got success")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); got != "Missing restore source" {
		t.Fatalf("summary = %q, want Missing restore source", got)
	}
	assertNoPost(t, capRT)
}
