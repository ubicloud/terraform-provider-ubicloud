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

func driveRead(t *testing.T, ctx context.Context, r *postgresResource, stateOver map[string]tftypes.Value) *resource.ReadResponse {
	t.Helper()
	schema := resource_postgres.PostgresResourceSchema(ctx)
	stateRaw := mkPGRaw(t, ctx, stateOver)
	req := resource.ReadRequest{State: tfsdk.State{Schema: schema, Raw: stateRaw}}
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: schema, Raw: stateRaw}}
	r.Read(ctx, req, resp)
	return resp
}

func driveCreate(t *testing.T, ctx context.Context, r *postgresResource, planOver map[string]tftypes.Value) *resource.CreateResponse {
	t.Helper()
	schema := resource_postgres.PostgresResourceSchema(ctx)
	planRaw := mkPGRaw(t, ctx, planOver)
	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: planRaw}}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schema, Raw: pgNullStateRaw(t, ctx)}}
	r.Create(ctx, req, resp)
	return resp
}

// configMapFromBody returns map[string]*string so a test can assert explicit-null
// (key-deletion) entries, not just present values.
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

// The detailed GET does not carry the config maps; Read must hydrate them from GET
// .../config or state is never authoritative and a config replace cannot compute removals.
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

// GET .../config can fail at creating-state (it computes default_pg_config off
// representative_server, which may not exist yet), so hydration must not fail the Read.
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
	if out.Flavor.ValueString() != "standard" {
		t.Errorf("flavor = %q, want standard (detail read must survive a config failure)", out.Flavor.ValueString())
	}
	pg := map[string]string{}
	out.PgConfig.ElementsAs(ctx, &pg, false)
	if pg["keep"] != "1" {
		t.Errorf("pg_config = %v, want prior {keep:1} preserved when config read is skipped", pg)
	}
}

// The server merges then compacts (existing.merge(supplied).compact): present keys
// overwrite, explicit nulls delete, so a dropped key must be sent as a null tombstone.
func TestPostgresConfigPatchBodyDeletesDroppedKeys(t *testing.T) {
	ctx := t.Context()
	var state, plan resource_postgres.PostgresModel
	state.PgConfig = mkStringMap(t, map[string]string{"max_connections": "100", "work_mem": "4MB"})
	plan.PgConfig = mkStringMap(t, map[string]string{"max_connections": "200"}) // work_mem dropped
	state.PgbouncerConfig = types.MapNull(types.StringType)
	plan.PgbouncerConfig = types.MapNull(types.StringType)

	body, diags := postgresConfigPatchBody(ctx, &plan, &state, nil)
	if diags.HasError() {
		t.Fatalf("diags=%+v", diags)
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

func TestPostgresConfigPatchBodyClearsMap(t *testing.T) {
	ctx := t.Context()
	var state, plan resource_postgres.PostgresModel
	state.PgConfig = mkStringMap(t, map[string]string{"max_connections": "100", "work_mem": "4MB"})
	plan.PgConfig = mkStringMap(t, map[string]string{}) // cleared
	state.PgbouncerConfig = types.MapNull(types.StringType)
	plan.PgbouncerConfig = types.MapNull(types.StringType)

	body, diags := postgresConfigPatchBody(ctx, &plan, &state, nil)
	if diags.HasError() {
		t.Fatalf("diags=%+v", diags)
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

func TestUpdateConfigReplaceImportStaleCompanionUntouched(t *testing.T) {
	ctx := t.Context()
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{
			"pg_config": rawConfigMap(map[string]string{"max_connections": "100", "work_mem": "4MB"}),
		},
		map[string]tftypes.Value{
			"pg_config": rawConfigMap(map[string]string{"max_connections": "200"}), // work_mem dropped
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

// State-only tombstones could miss a drifted key (the merge keeps it -> "inconsistent
// result after apply"), so an unavailable GET .../config must error before the PATCH.
func TestUpdateConfigChangeFailsClosedWhenConfigReadUnavailable(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{configStatus: http.StatusInternalServerError} // GET .../config 500s
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{"pg_config": rawConfigMap(map[string]string{"max_connections": "100"})},
		map[string]tftypes.Value{"pg_config": rawConfigMap(map[string]string{"max_connections": "200"})},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a config change with an unavailable config read must fail closed, got success")
	}
	if !diagHasSummary(resp.Diagnostics, "Unable to read current postgres config before update") {
		t.Errorf("want the fail-closed read summary, got %+v", resp.Diagnostics)
	}
	for _, rq := range capRT.reqs {
		if rq.Method == http.MethodPatch && strings.HasSuffix(rq.Path, "/config") {
			t.Errorf("config PATCH must NOT be issued when the pre-read failed (fail closed before mutating): %+v", capRT.reqs)
		}
	}
}

// The config pre-read runs BEFORE any mutation, so a mixed update whose pre-read fails
// must not land the tags PATCH first (a partial apply).
func TestUpdateMixedChangeFailsClosedBeforeAnyMutation(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{configStatus: http.StatusInternalServerError}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{
			"tags":      rawTags(t, ctx, [][2]string{{"team", "data"}}),
			"pg_config": rawConfigMap(map[string]string{"work_mem": "4MB"}),
		},
		map[string]tftypes.Value{
			"tags":      rawTags(t, ctx, [][2]string{{"team", "data"}, {"env", "prod"}}), // changed
			"pg_config": rawConfigMap(map[string]string{"work_mem": "8MB"}),              // changed
		},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a mixed update with an unavailable config read must fail closed")
	}
	if !diagHasSummary(resp.Diagnostics, "Unable to read current postgres config before update") {
		t.Errorf("want the fail-closed read summary, got %+v", resp.Diagnostics)
	}
	for _, rq := range capRT.dispatchReqs() {
		t.Errorf("no mutation may be issued when the config pre-read fails, got %s %s", rq.Method, rq.Path)
	}
}

// Post-apply state must hold the unchanged companion at its PLAN value, not the server
// drift: overlaying drift surfaces an element the plan never had -> "inconsistent result".
func TestUpdateConfigCompanionDriftNotOverlaid(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{configBody: &ubicloud_client.PostgresConfig{
		PgConfig:        map[string]string{"work_mem": "4MB"},
		PgbouncerConfig: map[string]string{"pool_mode": "transaction"}, // out-of-band companion drift
	}}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{
			"pg_config":        rawConfigMap(map[string]string{"work_mem": "4MB"}),
			"pgbouncer_config": rawConfigMap(map[string]string{}),
		},
		map[string]tftypes.Value{
			"pg_config":        rawConfigMap(map[string]string{"work_mem": "8MB"}), // changed
			"pgbouncer_config": rawConfigMap(map[string]string{}),                  // unchanged
		},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	for _, rq := range capRT.dispatchReqs() {
		if rq.Method == http.MethodPatch && strings.HasSuffix(rq.Path, "/config") {
			if _, has := configMapFromBody(t, rq.Body, "pgbouncer_config"); has {
				t.Errorf("unchanged pgbouncer_config must be omitted from the PATCH: %s", rq.Body)
			}
		}
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	pgb := map[string]string{}
	out.PgbouncerConfig.ElementsAs(ctx, &pgb, false)
	if len(pgb) != 0 {
		t.Errorf("pgbouncer_config = %v, want {} (plan value; server drift must NOT be overlaid)", pgb)
	}
	pg := map[string]string{}
	out.PgConfig.ElementsAs(ctx, &pg, false)
	if pg["work_mem"] != "8MB" || len(pg) != 1 {
		t.Errorf("pg_config = %v, want {work_mem:8MB} (plan value)", pg)
	}
}

// An unconfigured companion with a null prior is not pinned by UseStateForUnknown, so it
// plans as unknown; Update must resolve it to a known empty map, not persist unknown.
func TestUpdateConfigChangeResolvesUnknownCompanion(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{configBody: &ubicloud_client.PostgresConfig{
		PgConfig:        map[string]string{"work_mem": "4MB"},
		PgbouncerConfig: map[string]string{},
	}}
	r := newPostgresResourceWithRT(t, capRT)
	mapType := tftypes.Map{ElementType: tftypes.String}
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{
			"pg_config":        rawConfigMap(map[string]string{"work_mem": "4MB"}),
			"pgbouncer_config": tftypes.NewValue(mapType, nil), // null in state (hydration skipped)
		},
		map[string]tftypes.Value{
			"pg_config":        rawConfigMap(map[string]string{"work_mem": "8MB"}), // changed
			"pgbouncer_config": tftypes.NewValue(mapType, tftypes.UnknownValue),    // unpinned computed
		},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if out.PgbouncerConfig.IsUnknown() {
		t.Fatal("unchanged unconfigured companion must be resolved to a known value, not left unknown after apply")
	}
	pgb := map[string]string{}
	out.PgbouncerConfig.ElementsAs(ctx, &pgb, false)
	if len(pgb) != 0 {
		t.Errorf("companion = %v, want {} (ensurePostgresConfigKnown default)", pgb)
	}
}

// A live server key can be absent from stale state, so tombstones must derive from GET
// .../config, not state alone, or the merge keeps the key the plan meant to drop.
func TestUpdateConfigTombstonesServerKeyAbsentFromState(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{configBody: &ubicloud_client.PostgresConfig{
		PgConfig:        map[string]string{"work_mem": "4MB", "max_connections": "300"},
		PgbouncerConfig: map[string]string{},
	}}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{"pg_config": rawConfigMap(map[string]string{"work_mem": "4MB"})}, // stale: no max_connections
		map[string]tftypes.Value{"pg_config": rawConfigMap(map[string]string{"work_mem": "8MB"})}, // change work_mem; no max_connections declared
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}

	getIdx, patchIdx := -1, -1
	var patchBody string
	for i, rq := range capRT.reqs {
		if !strings.HasSuffix(rq.Path, "/config") {
			continue
		}
		if rq.Method == http.MethodGet && getIdx == -1 {
			getIdx = i
		}
		if rq.Method == http.MethodPatch && patchIdx == -1 {
			patchIdx, patchBody = i, rq.Body
		}
	}
	if getIdx == -1 {
		t.Fatalf("Update must GET .../config before the PATCH so tombstones derive from server keys; calls=%+v", capRT.reqs)
	}
	if patchIdx == -1 {
		t.Fatalf("expected a PATCH .../config; calls=%+v", capRT.reqs)
	}
	if getIdx > patchIdx {
		t.Fatalf("config GET must precede the PATCH, got GET@%d PATCH@%d", getIdx, patchIdx)
	}

	pg, has := configMapFromBody(t, patchBody, "pg_config")
	if !has {
		t.Fatalf("PATCH body must carry pg_config: %s", patchBody)
	}
	if pg["work_mem"] == nil || *pg["work_mem"] != "8MB" {
		t.Errorf("work_mem = %v, want \"8MB\": %s", pg["work_mem"], patchBody)
	}
	mc, present := pg["max_connections"]
	if !present {
		t.Fatalf("server key max_connections (absent from state, dropped by plan) must be tombstoned: %s", patchBody)
	}
	if mc != nil {
		t.Errorf("max_connections = %q, want null (tombstone): %s", *mc, patchBody)
	}
}

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

// withShortCreateTimeout lowers the create-timeout default for one test: the timeouts
// custom type exposes no settable attributes through the generated test schema.
func withShortCreateTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := postgresCreateTimeoutDefault
	postgresCreateTimeoutDefault = d
	t.Cleanup(func() { postgresCreateTimeoutDefault = prev })
}

func withFastAdoptBudget(t *testing.T, d time.Duration) {
	t.Helper()
	prev := postgresAdoptLookupBudget
	postgresAdoptLookupBudget = d
	t.Cleanup(func() { postgresAdoptLookupBudget = prev })
}

// timeouts.create must bound the WHOLE operation, including a stuck initial POST, not just
// the poll loop. Only r.Create runs in the goroutine; t.* calls stay on the test goroutine.
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
		// The post-timeout adopt lookup 404s on every attempt (the server never accepted the
		// create), so state stays empty for a clean recreate next apply.
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
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schema, Raw: pgNullStateRaw(t, ctx)}}

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
		if !resp.State.Raw.IsNull() {
			t.Errorf("a stuck POST the server never accepted must persist no state")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Create ignored its 100ms create timeout on a stuck POST (it hung)")
	}
}

// A client-side timeout does not prove the server skipped the create: empty state would
// orphan an accepted database into a name conflict, so Create adopts what the lookup finds.
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
		// created_at is stamped now so it postdates the dispatch and clears the adopt gate
		// (the sample fixture's fixed created_at predates the test run).
		pg := sampleDetailedPostgresResponse()
		pg.State = "creating"
		pg.CreatedAt = time.Now()
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
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schema, Raw: pgNullStateRaw(t, ctx)}}

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

// The adopt lookup is a bounded retry, not a one-shot GET: a transient 5xx or visibility
// lag would otherwise abandon a real, already-created database.
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
		if atomic.AddInt32(&getCalls, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":503,"message":"try again"}}`))
			return
		}
		// created_at is stamped now so this create's own row clears the adopt gate.
		pg := sampleDetailedPostgresResponse()
		pg.State = "creating"
		pg.CreatedAt = time.Now()
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
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schema, Raw: pgNullStateRaw(t, ctx)}}

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

// hydratePostgresConfig swallows transport errors, so a deadline landing on the final
// config read-back must still surface as a create timeout, not silent success.
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
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schema, Raw: pgNullStateRaw(t, ctx)}}

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
		if resp.State.Raw.IsNull() {
			t.Errorf("a dispatch that committed must persist the partial state to track and taint the row")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Create ignored its create timeout on a stuck config hydration (it hung)")
	}
}
