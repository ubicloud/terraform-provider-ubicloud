package provider

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func numRaw(n int64) tftypes.Value { return tftypes.NewValue(tftypes.Number, big.NewFloat(float64(n))) }

// driveRead runs the real resource Read against the given resource with a constructed
// state and returns the response (refreshed state + diagnostics).
func driveRead(t *testing.T, ctx context.Context, r *postgresResource, stateOver map[string]tftypes.Value) *resource.ReadResponse {
	t.Helper()
	schema := resource_postgres.PostgresResourceSchema(ctx)
	stateRaw := mkPGRaw(t, ctx, stateOver)
	req := resource.ReadRequest{State: tfsdk.State{Schema: schema, Raw: stateRaw}}
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: schema, Raw: stateRaw}}
	r.Read(ctx, req, resp)
	return resp
}

// driveCreate runs the real resource Create with a constructed plan and returns the
// response (new state + diagnostics).
func driveCreate(t *testing.T, ctx context.Context, r *postgresResource, planOver map[string]tftypes.Value) *resource.CreateResponse {
	t.Helper()
	schema := resource_postgres.PostgresResourceSchema(ctx)
	planRaw := mkPGRaw(t, ctx, planOver)
	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: planRaw}}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schema, Raw: planRaw}}
	r.Create(ctx, req, resp)
	return resp
}

// configMapFromBody pulls one config map out of a marshaled config PATCH body as
// map[string]*string, so a test can assert both present values and explicit-null
// (key-deletion) entries.
func configMapFromBody(t *testing.T, body string, key string) (map[string]*string, bool) {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &top); err != nil {
		t.Fatalf("config body is not a JSON object: %q (%v)", body, err)
	}
	raw, ok := top[key]
	if !ok {
		return nil, false
	}
	var m map[string]*string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("config map %q is not an object: %s", key, raw)
	}
	return m, true
}

// Read must hydrate pg_config/pgbouncer_config from the dedicated config endpoint: the
// detailed GET does not carry them, so without this read-back resource state is not
// authoritative for the maps and a declarative config replace cannot compute key removals.
func TestReadHydratesConfig(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{configBody: &ubicloud_client.PostgresConfig{
		PgConfig:        map[string]string{"max_connections": "200"},
		PgbouncerConfig: map[string]string{"pool_mode": "transaction"},
	}}
	r := newPostgresResourceWithRT(t, capRT)

	resp := driveRead(t, ctx, r, map[string]tftypes.Value{
		"pg_config": rawConfigMap(map[string]string{"stale": "1"}),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}

	var sawConfigGet bool
	for _, rq := range capRT.reqs {
		if rq.Method == http.MethodGet && strings.HasSuffix(rq.Path, "/config") {
			sawConfigGet = true
		}
	}
	if !sawConfigGet {
		t.Fatalf("Read must issue a GET .../config to hydrate config; calls=%+v", capRT.reqs)
	}

	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	pg := map[string]string{}
	out.PgConfig.ElementsAs(ctx, &pg, false)
	if pg["max_connections"] != "200" || len(pg) != 1 {
		t.Errorf("pg_config = %v, want {max_connections:200} from the read-back (not the stale state)", pg)
	}
	pgb := map[string]string{}
	out.PgbouncerConfig.ElementsAs(ctx, &pgb, false)
	if pgb["pool_mode"] != "transaction" || len(pgb) != 1 {
		t.Errorf("pgbouncer_config = %v, want {pool_mode:transaction} from the read-back", pgb)
	}
}

// Config hydration is best-effort: at creating-state GET .../config can fail (it computes
// default_pg_config off representative_server, which may not exist yet). A non-200 config
// read must NOT fail the whole Read, and the detail surface must still be mapped.
func TestReadConfigHydrationBestEffort(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{configStatus: http.StatusInternalServerError}
	r := newPostgresResourceWithRT(t, capRT)

	resp := driveRead(t, ctx, r, map[string]tftypes.Value{
		"pg_config": rawConfigMap(map[string]string{"keep": "1"}),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("config read failure must not fail Read: %+v", resp.Diagnostics)
	}

	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	// Detail surface still mapped.
	if out.Flavor.ValueString() != "standard" {
		t.Errorf("flavor = %q, want standard (detail read must survive a config failure)", out.Flavor.ValueString())
	}
	// Prior config left in place (not clobbered to null/empty) when hydration is skipped.
	pg := map[string]string{}
	out.PgConfig.ElementsAs(ctx, &pg, false)
	if pg["keep"] != "1" {
		t.Errorf("pg_config = %v, want prior {keep:1} preserved when config read is skipped", pg)
	}
}

// postgresConfigPatchBody must express a key the user dropped from a map as an explicit
// null, so the server merge (existing.merge(supplied).compact) strips it and the resulting
// map equals the plan exactly. Present keys carry their values; dropped keys carry null.
func TestPostgresConfigPatchBodyDeletesDroppedKeys(t *testing.T) {
	ctx := t.Context()
	var state, plan resource_postgres.PostgresModel
	state.PgConfig = mkStringMap(t, map[string]string{"max_connections": "100", "work_mem": "4MB"})
	plan.PgConfig = mkStringMap(t, map[string]string{"max_connections": "200"}) // work_mem dropped
	state.PgbouncerConfig = types.MapNull(types.StringType)
	plan.PgbouncerConfig = types.MapNull(types.StringType)

	body, ok, diags := postgresConfigPatchBody(ctx, &plan, &state)
	if diags.HasError() || !ok {
		t.Fatalf("ok=%v diags=%+v", ok, diags)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	pg, has := configMapFromBody(t, string(raw), "pg_config")
	if !has {
		t.Fatalf("body must carry pg_config: %s", raw)
	}
	if pg["max_connections"] == nil || *pg["max_connections"] != "200" {
		t.Errorf("max_connections = %v, want \"200\": %s", pg["max_connections"], raw)
	}
	wm, present := pg["work_mem"]
	if !present {
		t.Errorf("dropped key work_mem must be present as an explicit null to delete it: %s", raw)
	} else if wm != nil {
		t.Errorf("work_mem = %q, want null (key deletion): %s", *wm, raw)
	}
}

// Clearing a map to empty must null every prior key, so the server compact removes them all.
func TestPostgresConfigPatchBodyClearsMap(t *testing.T) {
	ctx := t.Context()
	var state, plan resource_postgres.PostgresModel
	state.PgConfig = mkStringMap(t, map[string]string{"max_connections": "100", "work_mem": "4MB"})
	plan.PgConfig = mkStringMap(t, map[string]string{}) // cleared
	state.PgbouncerConfig = types.MapNull(types.StringType)
	plan.PgbouncerConfig = types.MapNull(types.StringType)

	body, ok, diags := postgresConfigPatchBody(ctx, &plan, &state)
	if diags.HasError() || !ok {
		t.Fatalf("ok=%v diags=%+v", ok, diags)
	}
	raw, _ := json.Marshal(body)
	pg, has := configMapFromBody(t, string(raw), "pg_config")
	if !has {
		t.Fatalf("body must carry pg_config: %s", raw)
	}
	if len(pg) != 2 {
		t.Fatalf("cleared map must null all prior keys, got %v: %s", pg, raw)
	}
	for k, v := range pg {
		if v != nil {
			t.Errorf("key %q = %q, want null when clearing the map: %s", k, *v, raw)
		}
	}
}

// The import/stale headline: a single-map change on a resource whose companion map is
// absent from state (e.g. freshly imported, hydration deferred) must (a) delete the dropped
// key in the changed map and (b) NEVER send the unchanged companion map, so a one-map edit
// can never wipe live server-side companion config.
func TestUpdateConfigReplaceImportStaleCompanionUntouched(t *testing.T) {
	ctx := t.Context()
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{
			"pg_config": rawConfigMap(map[string]string{"max_connections": "100", "work_mem": "4MB"}),
			// pgbouncer_config absent from state (null): the companion the import never hydrated.
		},
		map[string]tftypes.Value{
			"pg_config": rawConfigMap(map[string]string{"max_connections": "200"}), // work_mem dropped
			// pgbouncer_config still omitted.
		},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	d := capRT.dispatchReqs()
	if len(d) != 1 || d[0].Method != http.MethodPatch || !strings.HasSuffix(d[0].Path, "/postgres/tf-acc-pg/config") {
		t.Fatalf("dispatch = %+v, want one PATCH .../config", d)
	}
	pg, has := configMapFromBody(t, d[0].Body, "pg_config")
	if !has {
		t.Fatalf("body must carry pg_config: %s", d[0].Body)
	}
	if pg["max_connections"] == nil || *pg["max_connections"] != "200" {
		t.Errorf("max_connections = %v, want 200: %s", pg["max_connections"], d[0].Body)
	}
	if wm, present := pg["work_mem"]; !present || wm != nil {
		t.Errorf("work_mem must be an explicit null to take the removal effect: %s", d[0].Body)
	}
	if _, has := configMapFromBody(t, d[0].Body, "pgbouncer_config"); has {
		t.Errorf("unchanged companion pgbouncer_config must be OMITTED (never wiped): %s", d[0].Body)
	}
}

// Under STALE changed-map state (the prior state misses a key the server still holds, so the
// request omits a tombstone for it), post-update state must reflect the SERVER's authoritative
// post-merge config (returned by the config PATCH), not the plan. Otherwise state would claim
// a key is gone while the server keeps it, until the next refresh reconciles.
func TestUpdateConfigStateMirrorsServerNotPlan(t *testing.T) {
	ctx := t.Context()
	// The server's post-merge truth: max_connections updated to 200, but work_mem survived
	// because stale state never tombstoned it; pgbouncer carries a value state did not know.
	capRT := &captureRT{configBody: &ubicloud_client.PostgresConfig{
		PgConfig:        map[string]string{"max_connections": "200", "work_mem": "4MB"},
		PgbouncerConfig: map[string]string{"pool_mode": "transaction"},
	}}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{"pg_config": rawConfigMap(map[string]string{"max_connections": "100"})}, // stale/partial
		map[string]tftypes.Value{"pg_config": rawConfigMap(map[string]string{"max_connections": "200"})},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	pg := map[string]string{}
	out.PgConfig.ElementsAs(ctx, &pg, false)
	if pg["max_connections"] != "200" || pg["work_mem"] != "4MB" || len(pg) != 2 {
		t.Errorf("pg_config = %v, want server truth {max_connections:200, work_mem:4MB} (not the plan)", pg)
	}
	pgb := map[string]string{}
	out.PgbouncerConfig.ElementsAs(ctx, &pgb, false)
	if pgb["pool_mode"] != "transaction" || len(pgb) != 1 {
		t.Errorf("pgbouncer_config = %v, want server truth {pool_mode:transaction}", pgb)
	}
}

// The create body must transmit pg_config/pgbouncer_config when set, so a create-with-config
// actually lands the overrides server-side (and the later hydration read agrees with state
// instead of drifting back to the server default).
func TestCreateBodyIncludesConfig(t *testing.T) {
	ctx := t.Context()
	r, capRT := newCapturingPostgresResource(t)
	capRT.detailState = "running" // Create blocks until the detail GET reports running
	resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
		"size":             strRaw("m8gd.large"),
		"storage_size":     numRaw(64),
		"pg_config":        rawConfigMap(map[string]string{"max_connections": "200"}),
		"pgbouncer_config": rawConfigMap(map[string]string{"pool_mode": "transaction"}),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}

	var create *capturedReq
	for i := range capRT.reqs {
		rq := capRT.reqs[i]
		if rq.Method == http.MethodPost && strings.HasSuffix(rq.Path, "/postgres/tf-acc-pg") {
			create = &capRT.reqs[i]
		}
	}
	if create == nil {
		t.Fatalf("expected a create POST .../postgres/tf-acc-pg; calls=%+v", capRT.reqs)
	}
	b := bodyJSON(t, create.Body)
	pg, ok := b["pg_config"].(map[string]any)
	if !ok || pg["max_connections"] != "200" {
		t.Errorf("create body pg_config = %v, want {max_connections:200}: %s", b["pg_config"], create.Body)
	}
	pgb, ok := b["pgbouncer_config"].(map[string]any)
	if !ok || pgb["pool_mode"] != "transaction" {
		t.Errorf("create body pgbouncer_config = %v, want {pool_mode:transaction}: %s", b["pgbouncer_config"], create.Body)
	}
}

// withShortCreateTimeout lowers the default create timeout for one test (restored on cleanup),
// so a test can exercise the create deadline without a 60m wait or plumbing a timeouts block
// (the timeouts custom type exposes no settable attributes through the generated test schema).
func withShortCreateTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := postgresCreateTimeoutDefault
	postgresCreateTimeoutDefault = d
	t.Cleanup(func() { postgresCreateTimeoutDefault = prev })
}

// withFastAdoptBudget shrinks the post-dispatch-timeout adopt lookup budget for one test, so a
// test can exercise the bounded adopt retry (and its give-up path) without a 30s wait.
func withFastAdoptBudget(t *testing.T, d time.Duration) {
	t.Helper()
	prev := postgresAdoptLookupBudget
	postgresAdoptLookupBudget = d
	t.Cleanup(func() { postgresAdoptLookupBudget = prev })
}

// Create must bound the WHOLE operation by timeouts.create, not just the poll loop: a stuck
// create POST is cancelled at the deadline rather than hanging on the provider-wide context
// (Codex HIGH). With a 100ms create timeout and a create endpoint that never responds, Create
// returns an error within the budget; without the operation-scoped context it hangs and the
// 3s watchdog fires. The request is built on the test goroutine (mkPGRaw may t.Fatal); only
// r.Create runs in the goroutine, which performs no t.* calls.
func TestCreateBoundsStuckPostByCreateTimeout(t *testing.T) {
	ctx := context.Background()
	withFastPoll(t)
	withShortCreateTimeout(t, 100*time.Millisecond)
	withFastAdoptBudget(t, 200*time.Millisecond)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			<-release // the create POST never responds
			return
		}
		// After the POST times out, Create makes a bounded adopt lookup; model a server that never
		// accepted the create (404 on every attempt) so the lookup finds nothing across its budget
		// and state stays empty (a clean recreate next apply).
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
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
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schema, Raw: planRaw}}

	done := make(chan struct{})
	go func() {
		r.Create(ctx, req, resp)
		close(done)
	}()
	select {
	case <-done:
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected a bounded create error from a stuck POST, got success")
		}
		if got := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(got, "Timeout while creating") {
			t.Fatalf("first-request timeout must be classified as a create timeout, got %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Create ignored its 100ms create timeout on a stuck POST (it hung)")
	}
}

// A create POST that times out client-side does not prove the server skipped the create: if the
// backend accepted it but the response was lost, returning empty state orphans the database into
// a name conflict on the next apply (Codex HIGH). Create must make a best-effort lookup and adopt
// an already-created database into state (tracked, replaced next apply) instead. Here the POST
// hangs past the 100ms create timeout while the detail GET reports the database exists; Create
// must return the create timeout AND persist the adopted server state.
func TestCreateAdoptsCreatedPostgresOnStuckPostTimeout(t *testing.T) {
	ctx := context.Background()
	withFastPoll(t) // also shortens the adopt lookup's one-poll-interval bound
	withShortCreateTimeout(t, 100*time.Millisecond)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			<-release // the create POST never responds; the deadline must surface as a timeout
			return
		}
		// The best-effort adopt lookup: the server DID accept the create (db exists, still creating).
		pg := sampleDetailedPostgresResponse()
		pg.State = "creating"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(pg)
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
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schema, Raw: planRaw}}

	done := make(chan struct{})
	go func() {
		r.Create(ctx, req, resp)
		close(done)
	}()
	select {
	case <-done:
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected a bounded create timeout, got success")
		}
		if got := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(got, "Timeout while creating") {
			t.Fatalf("expected a create timeout, got %q", got)
		}
		var saved resource_postgres.PostgresModel
		if d := resp.State.Get(ctx, &saved); d.HasError() {
			t.Fatalf("reading persisted state: %v", d)
		}
		if got := saved.Id.ValueString(); got != "pgn30gjk1d1e2jj34v9x0dq4rp" {
			t.Fatalf("timed-out create did not adopt the already-created database (state id=%q): it is orphaned", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Create hung instead of bounding the stuck POST and adopting the created database")
	}
}

// The adopt lookup must be a BOUNDED RETRY, not a one-shot GET (Codex MEDIUM): a single transient
// failure (a 5xx, a network blip) or a brief post-accept visibility lag would otherwise abandon a
// real, already-created database and orphan it into a next-apply name conflict. Here the create
// POST hangs past the create timeout, the first adopt GET returns 503, and the retry sees the
// created database; Create must still adopt it (state carries the server id).
func TestCreateAdoptsCreatedPostgresAfterTransientLookupFailure(t *testing.T) {
	ctx := context.Background()
	withFastPoll(t)
	withShortCreateTimeout(t, 100*time.Millisecond)
	withFastAdoptBudget(t, 2*time.Second)
	release := make(chan struct{})
	var getCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			<-release // the create POST never responds; the deadline must surface as a timeout
			return
		}
		// First adopt lookup fails transiently; the retry must see the already-created database.
		if atomic.AddInt32(&getCalls, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":503,"message":"try again"}}`))
			return
		}
		pg := sampleDetailedPostgresResponse()
		pg.State = "creating"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(pg)
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
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schema, Raw: planRaw}}

	done := make(chan struct{})
	go func() {
		r.Create(ctx, req, resp)
		close(done)
	}()
	select {
	case <-done:
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected a bounded create timeout, got success")
		}
		var saved resource_postgres.PostgresModel
		if d := resp.State.Get(ctx, &saved); d.HasError() {
			t.Fatalf("reading persisted state: %v", d)
		}
		if got := saved.Id.ValueString(); got != "pgn30gjk1d1e2jj34v9x0dq4rp" {
			t.Fatalf("adopt gave up after a transient lookup failure (state id=%q): created db orphaned", got)
		}
		if got := atomic.LoadInt32(&getCalls); got < 2 {
			t.Fatalf("adopt must retry past the transient failure, but made only %d GET(s)", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Create hung instead of bounding the stuck POST and adopting after a transient lookup failure")
	}
}

// hydratePostgresConfig is best-effort and swallows transport errors, so a deadline that lands
// on the final /config read-back must still surface as a create timeout, not silent success
// (Codex MEDIUM). The database reaches running, then the config GET hangs past the 150ms create
// timeout; Create must return a "Timeout while creating" error, not success.
func TestCreateBoundsStuckConfigHydrationByCreateTimeout(t *testing.T) {
	ctx := context.Background()
	withFastPoll(t)
	withShortCreateTimeout(t, 150*time.Millisecond)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/config") {
			<-release // config hydration never responds; the deadline must surface as a timeout
			return
		}
		pg := sampleDetailedPostgresResponse() // create POST + readiness GET both report running
		pg.State = "running"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pg)
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
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schema, Raw: planRaw}}

	done := make(chan struct{})
	go func() {
		r.Create(ctx, req, resp)
		close(done)
	}()
	select {
	case <-done:
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected a create timeout when config hydration hangs past the deadline, got success")
		}
		if got := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(got, "Timeout while creating") {
			t.Fatalf("a swallowed hydration deadline must surface as a create timeout, got %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Create ignored its create timeout on a stuck config hydration (it hung)")
	}
}
