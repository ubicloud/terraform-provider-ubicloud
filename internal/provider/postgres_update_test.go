package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// capturedReq is one HTTP round trip the resource issued, recorded at the network
// boundary (the generated ubicloud_client is never stubbed; only its transport is).
type capturedReq struct {
	Method string
	Path   string
	Body   string
}

// captureRT records every request and returns a canned, schema-valid response so the
// real generated client parses it without error. Detail/PATCH/rename echo a detailed
// PostgresDatabase; the config endpoint echoes a PostgresConfig. detailGetStatus, when
// non-zero, overrides the status of the trailing detail GET so a test can simulate a
// read that fails after the mutations already landed.
type captureRT struct {
	reqs            []capturedReq
	detailGetStatus int
	// configStatus, when non-zero, overrides the status of a GET on the config endpoint so
	// a test can simulate the config read-back failing (as it can at creating-state).
	configStatus int
	// configBody, when non-nil, is echoed by the config endpoint instead of the default, so
	// a test controls exactly what pg_config/pgbouncer_config the hydration read returns.
	configBody *ubicloud_client.PostgresConfig
	// detailBody, when non-nil, is echoed by the detail/PATCH/rename endpoint instead of the
	// default sample, so a test controls exactly what the read surface reports (e.g. a
	// version that still lags target_version mid-upgrade).
	detailBody *ubicloud_client.PostgresDatabase
	// upgradeStatus, when non-empty, is the upgrade_status the /upgrade endpoint reports
	// (default "running"); a test sets "failed" to exercise the failed-upgrade surfacing.
	upgradeStatus string
	// upgradeStatusCode, when non-zero, overrides the HTTP status the /upgrade endpoint
	// returns (e.g. 500 to exercise fail-closed, or 400 = "Database is not upgrading" = the
	// upgrade already converged).
	upgradeStatusCode int
	// upgradeTransportErr makes the /upgrade endpoint fail at the transport layer, so a test
	// can exercise the fail-closed status read.
	upgradeTransportErr bool
}

func (c *captureRT) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		req.Body.Close()
	}
	c.reqs = append(c.reqs, capturedReq{Method: req.Method, Path: req.URL.Path, Body: string(body)})

	if c.upgradeTransportErr && strings.HasSuffix(req.URL.Path, "/upgrade") {
		return nil, errors.New("simulated upgrade-status transport error")
	}

	status := http.StatusOK
	var respBody []byte
	switch {
	case strings.HasSuffix(req.URL.Path, "/config"):
		switch {
		case req.Method == http.MethodGet && c.configStatus != 0:
			status = c.configStatus
			respBody = []byte(`{"error":{"message":"simulated config read failure"}}`)
		case c.configBody != nil:
			respBody, _ = json.Marshal(*c.configBody)
		default:
			respBody, _ = json.Marshal(ubicloud_client.PostgresConfig{
				PgConfig:        map[string]string{"max_connections": "100"},
				PgbouncerConfig: map[string]string{},
			})
		}
	case strings.HasSuffix(req.URL.Path, "/upgrade"):
		if c.upgradeStatusCode != 0 {
			status = c.upgradeStatusCode
		}
		upgradeStatus := c.upgradeStatus
		if upgradeStatus == "" {
			upgradeStatus = "running"
		}
		respBody, _ = json.Marshal(ubicloud_client.PostgresDatabaseUpgradeStatus{
			CurrentVersion: ubicloud_client.PostgresDatabaseUpgradeStatusCurrentVersionN16,
			TargetVersion:  ubicloud_client.PostgresDatabaseUpgradeStatusTargetVersionN17,
			UpgradeStatus:  upgradeStatus,
		})
	default:
		switch {
		case req.Method == http.MethodGet && c.detailGetStatus != 0:
			status = c.detailGetStatus
			respBody = []byte(`{"error":{"message":"simulated read failure"}}`)
		case c.detailBody != nil:
			respBody, _ = json.Marshal(*c.detailBody)
		default:
			respBody, _ = json.Marshal(sampleDetailedPostgresResponse())
		}
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(respBody)),
		Request:    req,
	}, nil
}

// dispatchReqs drops the trailing detail GET (every successful Update re-reads), so a
// test asserts only the mutation calls it triggered.
func (c *captureRT) dispatchReqs() []capturedReq {
	var out []capturedReq
	for _, r := range c.reqs {
		if r.Method == http.MethodGet {
			continue
		}
		out = append(out, r)
	}
	return out
}

func newPostgresResourceWithRT(t *testing.T, capRT *captureRT) *postgresResource {
	t.Helper()
	hc := &http.Client{Transport: capRT}
	client, err := ubicloud_client.NewClientWithResponses("http://unit.test", ubicloud_client.WithHTTPClient(hc))
	if err != nil {
		t.Fatalf("NewClientWithResponses: %v", err)
	}
	return &postgresResource{uc: &UbicloudClient{client: client, endpoint: "http://unit.test"}}
}

func newCapturingPostgresResource(t *testing.T) (*postgresResource, *captureRT) {
	t.Helper()
	capRT := &captureRT{}
	return newPostgresResourceWithRT(t, capRT), capRT
}

func strRaw(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }

// rawTags builds a tftypes value for the resource schema's tags list attribute.
func rawTags(t *testing.T, ctx context.Context, pairs [][2]string) tftypes.Value {
	t.Helper()
	objType := postgresResourceSchemaObjType(t, ctx)
	listType, ok := objType.AttributeTypes["tags"].(tftypes.List)
	if !ok {
		t.Fatalf("tags attr type is not a list: %T", objType.AttributeTypes["tags"])
	}
	elemType := listType.ElementType
	elems := make([]tftypes.Value, 0, len(pairs))
	for _, p := range pairs {
		elems = append(elems, tftypes.NewValue(elemType, map[string]tftypes.Value{
			"key":   strRaw(p[0]),
			"value": strRaw(p[1]),
		}))
	}
	return tftypes.NewValue(listType, elems)
}

func rawConfigMap(kv map[string]string) tftypes.Value {
	mapType := tftypes.Map{ElementType: tftypes.String}
	elems := make(map[string]tftypes.Value, len(kv))
	for k, v := range kv {
		elems[k] = strRaw(v)
	}
	return tftypes.NewValue(mapType, elems)
}

// mkPGRaw builds a resource object value with project_id/location/name defaulted (so the
// request paths are well-formed) and the supplied overrides applied on top.
func mkPGRaw(t *testing.T, ctx context.Context, over map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	base := map[string]tftypes.Value{
		"project_id": strRaw("pjtest"),
		"location":   strRaw("aws-us-east-1"),
		"name":       strRaw("tf-acc-pg"),
	}
	for k, v := range over {
		base[k] = v
	}
	return postgresRaw(t, ctx, base)
}

// driveUpdate runs the real resource Update with constructed plan/state against the given
// resource and returns the response (state + diagnostics).
func driveUpdate(t *testing.T, ctx context.Context, r *postgresResource, stateOver, planOver map[string]tftypes.Value) *resource.UpdateResponse {
	t.Helper()
	schema := resource_postgres.PostgresResourceSchema(ctx)
	stateRaw := mkPGRaw(t, ctx, stateOver)
	planRaw := mkPGRaw(t, ctx, planOver)
	req := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schema, Raw: planRaw},
		State: tfsdk.State{Schema: schema, Raw: stateRaw},
	}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: schema, Raw: stateRaw}}
	r.Update(ctx, req, resp)
	return resp
}

// runUpdate drives Update with a capturing transport and returns the captured traffic
// plus the response.
func runUpdate(t *testing.T, ctx context.Context, stateOver, planOver map[string]tftypes.Value) (*captureRT, *resource.UpdateResponse) {
	t.Helper()
	r, capRT := newCapturingPostgresResource(t)
	return capRT, driveUpdate(t, ctx, r, stateOver, planOver)
}

func bodyJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("request body is not JSON object: %q (%v)", raw, err)
	}
	return m
}

// A tags-only change must issue exactly one PATCH carrying only tags (resize/HA fields
// omitted), then the detail re-read. No rename, no config.
func TestUpdateDispatchTagsOnly(t *testing.T) {
	ctx := t.Context()
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{"tags": rawTags(t, ctx, [][2]string{{"team", "data"}})},
		map[string]tftypes.Value{"tags": rawTags(t, ctx, [][2]string{{"team", "data"}, {"env", "prod"}})},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	d := capRT.dispatchReqs()
	if len(d) != 1 {
		t.Fatalf("dispatch calls = %d (%+v), want 1 PATCH", len(d), d)
	}
	if d[0].Method != http.MethodPatch || !strings.HasSuffix(d[0].Path, "/postgres/tf-acc-pg") {
		t.Fatalf("call = %s %s, want PATCH .../postgres/tf-acc-pg", d[0].Method, d[0].Path)
	}
	b := bodyJSON(t, d[0].Body)
	if _, ok := b["tags"]; !ok {
		t.Errorf("PATCH body missing tags: %s", d[0].Body)
	}
	for _, k := range []string{"size", "storage_size", "ha_type"} {
		if _, ok := b[k]; ok {
			t.Errorf("PATCH body must omit unchanged %q: %s", k, d[0].Body)
		}
	}
}

// A size-only change issues a PATCH with size set; convergence is not asserted here.
func TestUpdateDispatchSizeOnly(t *testing.T) {
	ctx := t.Context()
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{"size": strRaw("m8gd.large")},
		map[string]tftypes.Value{"size": strRaw("m8gd.xlarge")},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	d := capRT.dispatchReqs()
	if len(d) != 1 || d[0].Method != http.MethodPatch {
		t.Fatalf("dispatch = %+v, want one PATCH", d)
	}
	b := bodyJSON(t, d[0].Body)
	if b["size"] != "m8gd.xlarge" {
		t.Errorf("PATCH size = %v, want m8gd.xlarge: %s", b["size"], d[0].Body)
	}
}

// A name change issues a rename (old name in the path, new name in the body) and the
// trailing detail GET targets the NEW name.
func TestUpdateDispatchNameOnly(t *testing.T) {
	ctx := t.Context()
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{},
		map[string]tftypes.Value{"name": strRaw("tf-acc-pg-renamed")},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	d := capRT.dispatchReqs()
	if len(d) != 1 || d[0].Method != http.MethodPost || !strings.HasSuffix(d[0].Path, "/postgres/tf-acc-pg/rename") {
		t.Fatalf("dispatch = %+v, want POST .../postgres/tf-acc-pg/rename", d)
	}
	if b := bodyJSON(t, d[0].Body); b["name"] != "tf-acc-pg-renamed" {
		t.Errorf("rename body name = %v, want tf-acc-pg-renamed", b["name"])
	}
	var sawNewNameGet bool
	for _, r := range capRT.reqs {
		if r.Method == http.MethodGet && strings.HasSuffix(r.Path, "/postgres/tf-acc-pg-renamed") {
			sawNewNameGet = true
		}
	}
	if !sawNewNameGet {
		t.Errorf("expected a detail GET on the new name; calls=%+v", capRT.reqs)
	}
}

// When the rename has already landed on the server but the follow-up detail read fails,
// Update must still persist the NEW name, so a later refresh addresses the renamed row
// instead of 404-ing on the old name and planning a spurious recreate.
func TestUpdateRenamePersistsNameWhenReadFails(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{detailGetStatus: http.StatusInternalServerError}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r, nil, map[string]tftypes.Value{"name": strRaw("tf-acc-pg-renamed")})

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected a read-after-update error when the detail GET fails")
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if out.Name.ValueString() != "tf-acc-pg-renamed" {
		t.Errorf("state name = %q, want the new name persisted despite the failed re-read", out.Name.ValueString())
	}
}

// A pg_config-only change issues the config MERGE (PATCH .../config) carrying only
// pg_config. pgbouncer_config is UNCHANGED here (unset in both state and plan, as on a
// freshly imported resource the detail GET never hydrated), so it must be OMITTED: the
// merge endpoint leaves an omitted map untouched, where a full replace would send {} and
// wipe live server-side pgbouncer config (Codex HIGH).
func TestUpdateDispatchConfigOnly(t *testing.T) {
	ctx := t.Context()
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{"pg_config": rawConfigMap(map[string]string{"max_connections": "100"})},
		map[string]tftypes.Value{"pg_config": rawConfigMap(map[string]string{"max_connections": "200"})},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	d := capRT.dispatchReqs()
	if len(d) != 1 || d[0].Method != http.MethodPatch || !strings.HasSuffix(d[0].Path, "/postgres/tf-acc-pg/config") {
		t.Fatalf("dispatch = %+v, want PATCH .../postgres/tf-acc-pg/config", d)
	}
	b := bodyJSON(t, d[0].Body)
	pg, ok := b["pg_config"].(map[string]any)
	if !ok || pg["max_connections"] != "200" {
		t.Errorf("config body pg_config = %v, want max_connections=200: %s", b["pg_config"], d[0].Body)
	}
	if _, ok := b["pgbouncer_config"]; ok {
		t.Errorf("unchanged pgbouncer_config must be OMITTED (merge preserves it), got: %s", d[0].Body)
	}
}

// Combined change proves ordering: content mutations (PATCH, config) run under the OLD
// name, then rename last, then the detail GET on the NEW name.
func TestUpdateDispatchCombinedOrdering(t *testing.T) {
	ctx := t.Context()
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{
			"tags":      rawTags(t, ctx, [][2]string{{"team", "data"}}),
			"pg_config": rawConfigMap(map[string]string{"max_connections": "100"}),
		},
		map[string]tftypes.Value{
			"name":      strRaw("tf-acc-pg-renamed"),
			"tags":      rawTags(t, ctx, [][2]string{{"team", "data"}, {"env", "prod"}}),
			"pg_config": rawConfigMap(map[string]string{"max_connections": "200"}),
		},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	d := capRT.dispatchReqs()
	if len(d) != 3 {
		t.Fatalf("dispatch calls = %d (%+v), want PATCH, config, rename", len(d), d)
	}
	if d[0].Method != http.MethodPatch || !strings.HasSuffix(d[0].Path, "/postgres/tf-acc-pg") {
		t.Errorf("call[0] = %s %s, want PATCH on old name", d[0].Method, d[0].Path)
	}
	if d[1].Method != http.MethodPatch || !strings.HasSuffix(d[1].Path, "/postgres/tf-acc-pg/config") {
		t.Errorf("call[1] = %s %s, want config PATCH on old name", d[1].Method, d[1].Path)
	}
	if d[2].Method != http.MethodPost || !strings.HasSuffix(d[2].Path, "/postgres/tf-acc-pg/rename") {
		t.Errorf("call[2] = %s %s, want rename last", d[2].Method, d[2].Path)
	}
}

// A one-major version increase dispatches the imperative upgrade (POST .../upgrade) and
// NOTHING else: version is not a PATCH field. The target major is server-chosen
// (target_version = version + 1), so the POST carries no body.
func TestUpdateVersionChangeDispatchesUpgrade(t *testing.T) {
	ctx := t.Context()
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{"version": strRaw("16")},
		map[string]tftypes.Value{"version": strRaw("17")},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	d := capRT.dispatchReqs()
	if len(d) != 1 || d[0].Method != http.MethodPost || !strings.HasSuffix(d[0].Path, "/postgres/tf-acc-pg/upgrade") {
		t.Fatalf("dispatch = %+v, want one POST .../postgres/tf-acc-pg/upgrade", d)
	}
	if strings.TrimSpace(d[0].Body) != "" {
		t.Errorf("upgrade is a no-body POST (target is server-chosen), got body: %q", d[0].Body)
	}
}

// A multi-major jump cannot be expressed as one server-chosen upgrade (which advances
// exactly one major); it must be rejected before any mutation lands (no half-apply, no
// REST call at all).
func TestUpdateVersionMultiMajorRejected(t *testing.T) {
	ctx := t.Context()
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{"version": strRaw("16")},
		map[string]tftypes.Value{"version": strRaw("18")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("multi-major version jump must error")
	}
	if len(capRT.reqs) != 0 {
		t.Errorf("rejected jump must issue no REST calls, got %+v", capRT.reqs)
	}
}

// A downgrade is unsupported and must be rejected before any mutation, with no REST call.
func TestUpdateVersionDowngradeRejected(t *testing.T) {
	ctx := t.Context()
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{"version": strRaw("17")},
		map[string]tftypes.Value{"version": strRaw("16")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("version downgrade must error")
	}
	if len(capRT.reqs) != 0 {
		t.Errorf("rejected downgrade must issue no REST calls, got %+v", capRT.reqs)
	}
}

// When an upgrade is already in flight toward the planned version (server advanced
// target_version, actual version still lagging), Update must NOT issue a second POST
// .../upgrade (the backend rejects it until convergence). It must instead query GET
// .../upgrade to confirm the upgrade has not failed, and reconcile by holding the planned
// version.
func TestUpdateVersionInFlightSkipsUpgrade(t *testing.T) {
	ctx := t.Context()
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{"version": strRaw("16"), "target_version": strRaw("17")},
		map[string]tftypes.Value{"version": strRaw("17")},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	var sawStatusGet bool
	for _, r := range capRT.reqs {
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/upgrade") {
			t.Errorf("in-flight upgrade must not re-POST .../upgrade, got %+v", capRT.reqs)
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.Path, "/upgrade") {
			sawStatusGet = true
		}
	}
	if !sawStatusGet {
		t.Errorf("in-flight skip must query GET .../upgrade to detect a failed upgrade; calls=%+v", capRT.reqs)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if out.Version.ValueString() != "17" {
		t.Errorf("state version = %q, want planned 17 held during in-flight upgrade", out.Version.ValueString())
	}
}

// A FAILED in-flight upgrade must surface an error, not be silently held as perpetual
// progress: the in-flight skip queries GET .../upgrade and raises when upgrade_status is
// "failed" (still target_version != version, but stuck), and must not re-POST.
func TestUpdateVersionInFlightFailedSurfacesError(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{upgradeStatus: "failed"}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{"version": strRaw("16"), "target_version": strRaw("17")},
		map[string]tftypes.Value{"version": strRaw("17")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed in-flight upgrade must surface an error, not be masked as progress")
	}
	for _, rec := range capRT.reqs {
		if rec.Method == http.MethodPost && strings.HasSuffix(rec.Path, "/upgrade") {
			t.Errorf("a failed upgrade must not be re-POSTed, got %+v", capRT.reqs)
		}
	}
}

// On the in-flight skip path the status read must FAIL CLOSED: when GET .../upgrade does not
// return a clean result, Update must not proceed to report success (which would hold the
// planned version and re-mask a possibly-failed upgrade). A non-200 status raises.
func TestUpdateVersionInFlightStatusUnreadableFailsClosed(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{upgradeStatusCode: http.StatusInternalServerError}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{"version": strRaw("16"), "target_version": strRaw("17")},
		map[string]tftypes.Value{"version": strRaw("17")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("an unreadable upgrade status must fail closed, not silently report success")
	}
}

// Fail-closed also covers a transport error on the status read.
func TestUpdateVersionInFlightStatusTransportErrorFailsClosed(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{upgradeTransportErr: true}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{"version": strRaw("16"), "target_version": strRaw("17")},
		map[string]tftypes.Value{"version": strRaw("17")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a transport error on the upgrade-status read must fail closed")
	}
}

// A 400 on the status read means "Database is not upgrading": the upgrade already converged
// (success). That is NOT a failure, so Update proceeds and holds the planned version.
func TestUpdateVersionInFlightConvergedProceeds(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{upgradeStatusCode: http.StatusBadRequest}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{"version": strRaw("16"), "target_version": strRaw("17")},
		map[string]tftypes.Value{"version": strRaw("17")},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a converged (400 not-upgrading) status must not error: %+v", resp.Diagnostics)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if out.Version.ValueString() != "17" {
		t.Errorf("state version = %q, want planned 17 held after convergence", out.Version.ValueString())
	}
}

// A version change combined with any other mutable change must be rejected before any REST
// call: a major upgrade is irreversible, so dispatching it then erroring on a later PATCH
// would strand it. (The ubi CLI likewise separates `upgrade` from `modify`.)
func TestUpdateVersionWithOtherChangeRejected(t *testing.T) {
	ctx := t.Context()
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{"version": strRaw("16"), "size": strRaw("m8gd.large")},
		map[string]tftypes.Value{"version": strRaw("17"), "size": strRaw("m8gd.xlarge")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("version change combined with a size change must be rejected")
	}
	if len(capRT.reqs) != 0 {
		t.Errorf("rejected combined change must issue no REST calls, got %+v", capRT.reqs)
	}
}

// After dispatching an upgrade the detail re-read still reports the OLD (lagging) version
// until convergence; the post-apply state must equal the REQUESTED version (the plan), or
// the framework raises an inconsistent-result error. target_version carries the in-flight
// target. Mirrors TestUpdateSizeReadbackKeepsRequestedSize.
func TestUpdateVersionReadbackKeepsRequestedVersion(t *testing.T) {
	ctx := t.Context()
	lagging := sampleDetailedPostgresResponse()
	lagging.Version = ubicloud_client.PostgresDatabaseVersionN16
	lagging.TargetVersion = ubicloud_client.PostgresDatabaseTargetVersionN17
	capRT := &captureRT{detailBody: &lagging}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{"version": strRaw("16")},
		map[string]tftypes.Value{"version": strRaw("17")},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if got := out.Version.ValueString(); got != "17" {
		t.Errorf("state version = %q, want requested 17 (held until convergence)", got)
	}
	if got := out.TargetVersion.ValueString(); got != "17" {
		t.Errorf("state target_version = %q, want in-flight target 17", got)
	}
}

// driveModifyPlan runs the real resource ModifyPlan with a constructed plan/state (the
// update path: prior state is non-null), returning the response so a test can assert the
// plan-time diagnostics.
func driveModifyPlan(t *testing.T, ctx context.Context, stateOver, planOver map[string]tftypes.Value) *resource.ModifyPlanResponse {
	t.Helper()
	schema := resource_postgres.PostgresResourceSchema(ctx)
	stateRaw := mkPGRaw(t, ctx, stateOver)
	planRaw := mkPGRaw(t, ctx, planOver)
	req := resource.ModifyPlanRequest{
		Config: tfsdk.Config{Schema: schema, Raw: planRaw},
		State:  tfsdk.State{Schema: schema, Raw: stateRaw},
		Plan:   tfsdk.Plan{Schema: schema, Raw: planRaw},
	}
	resp := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: schema, Raw: planRaw}}
	(&postgresResource{}).ModifyPlan(ctx, req, resp)
	return resp
}

// A multi-major jump is rejected at PLAN time (the upgrade endpoint advances exactly one
// major, server-chosen), so terraform plan fails before any apply.
func TestModifyPlanVersionMultiMajorRejected(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"version": strRaw("16")},
		map[string]tftypes.Value{"version": strRaw("18")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("multi-major version jump must be rejected at plan time")
	}
}

// A one-major increase is a valid upgrade and must NOT be rejected at plan time.
func TestModifyPlanOneMajorUpgradeAllowed(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"version": strRaw("16")},
		map[string]tftypes.Value{"version": strRaw("17")},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("one-major upgrade must be allowed at plan time: %+v", resp.Diagnostics)
	}
}

// A version change combined with another mutable change is rejected at PLAN time, so the
// user separates the upgrade before any apply runs.
func TestModifyPlanVersionWithOtherChangeRejected(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"version": strRaw("16"), "size": strRaw("m8gd.large")},
		map[string]tftypes.Value{"version": strRaw("17"), "size": strRaw("m8gd.xlarge")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("combined version+size change must be rejected at plan time")
	}
}

// No mutable change must call nothing (a refresh that only recomputed invariant
// computeds must not issue mutations or a re-read).
func TestUpdateNoMutableChangeCallsNothing(t *testing.T) {
	ctx := t.Context()
	same := map[string]tftypes.Value{"tags": rawTags(t, ctx, [][2]string{{"team", "data"}})}
	capRT, resp := runUpdate(t, ctx, same, same)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	if len(capRT.reqs) != 0 {
		t.Errorf("no-op Update must issue no calls, got %+v", capRT.reqs)
	}
}

// After a resize the post-apply state must equal the REQUESTED size (the plan), not the
// lagging actual vm_size the detail re-read returns, or the framework raises an
// inconsistent-result error. vm_size keeps the actual; size keeps the request.
func TestUpdateSizeReadbackKeepsRequestedSize(t *testing.T) {
	ctx := t.Context()
	// The mock detail response reports vm_size = m8gd.large (the pre-convergence actual).
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{"size": strRaw("m8gd.large")},
		map[string]tftypes.Value{"size": strRaw("m8gd.xlarge")},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	if len(capRT.dispatchReqs()) != 1 {
		t.Fatalf("want one PATCH, got %+v", capRT.dispatchReqs())
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if got := out.Size.ValueString(); got != "m8gd.xlarge" {
		t.Errorf("state size = %q, want requested m8gd.xlarge (not lagging vm_size)", got)
	}
	if got := out.VmSize.ValueString(); got != "m8gd.large" {
		t.Errorf("state vm_size = %q, want actual m8gd.large from the read-back", got)
	}
}

// postgresPatchBody sends only the changed resize/HA/tags fields and reports ok=false when
// nothing in the PATCH set changed (so an unrelated change, e.g. a rename, issues no PATCH).
func TestPostgresPatchBodyOnlyChangedFields(t *testing.T) {
	ctx := t.Context()
	var state resource_postgres.PostgresModel
	state.Size = types.StringValue("m8gd.large")
	state.StorageSize = types.Int64Value(118)
	state.HaType = types.StringValue("none")
	state.Tags = mkResourceTags(t, [][2]string{{"team", "data"}})

	// Only storage_size changes.
	plan := state
	plan.StorageSize = types.Int64Value(256)
	body, ok, diags := postgresPatchBody(ctx, &plan, &state)
	if diags.HasError() {
		t.Fatalf("diags: %+v", diags)
	}
	if !ok {
		t.Fatal("ok = false, want true when storage_size changed")
	}
	if body.StorageSize == nil || *body.StorageSize != 256 {
		t.Errorf("storage_size = %v, want 256", body.StorageSize)
	}
	if body.Size != nil || body.HaType != nil || body.Tags != nil {
		t.Errorf("unchanged fields must be omitted: size=%v ha=%v tags=%v", body.Size, body.HaType, body.Tags)
	}

	// Nothing in the PATCH set changes.
	if _, ok, _ := postgresPatchBody(ctx, &state, &state); ok {
		t.Error("ok = true with no PATCH-set change, want false")
	}
}

// configPatchKeys marshals the merge body and returns its top-level keys, so a test can
// assert which maps are sent (and thus which the server merges vs leaves untouched).
func configPatchKeys(t *testing.T, body ubicloud_client.PatchPostgresDatabaseConfigJSONRequestBody) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal patch body: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal patch body: %v", err)
	}
	return m
}

// postgresConfigPatchBody includes ONLY the changed map, so the merge endpoint leaves the
// companion untouched. This is the load-bearing safety property: a full replace would send
// the untouched map as {} and wipe live server config on an imported/stale resource.
func TestPostgresConfigPatchBodyOnlyChangedMaps(t *testing.T) {
	ctx := t.Context()
	withPg := func(m resource_postgres.PostgresModel, kv map[string]string) resource_postgres.PostgresModel {
		m.PgConfig = mkStringMap(t, kv)
		return m
	}

	// pg_config changed, pgbouncer unset/unchanged -> body carries pg_config only.
	var statePg, planPg resource_postgres.PostgresModel
	statePg = withPg(statePg, map[string]string{"max_connections": "100"})
	planPg = withPg(planPg, map[string]string{"max_connections": "200"})
	statePg.PgbouncerConfig = types.MapNull(types.StringType)
	planPg.PgbouncerConfig = types.MapNull(types.StringType)
	body, ok, diags := postgresConfigPatchBody(ctx, &planPg, &statePg)
	if diags.HasError() || !ok {
		t.Fatalf("pg-only: ok=%v diags=%+v", ok, diags)
	}
	keys := configPatchKeys(t, body)
	if _, has := keys["pg_config"]; !has {
		t.Errorf("pg-only body must carry pg_config: %v", keys)
	}
	if _, has := keys["pgbouncer_config"]; has {
		t.Errorf("pg-only body must OMIT pgbouncer_config (merge preserves it): %v", keys)
	}

	// pgbouncer_config changed, pg_config unchanged -> body carries pgbouncer_config only.
	var statePgb, planPgb resource_postgres.PostgresModel
	statePgb.PgConfig = types.MapNull(types.StringType)
	planPgb.PgConfig = types.MapNull(types.StringType)
	statePgb.PgbouncerConfig = mkStringMap(t, map[string]string{"pool_mode": "session"})
	planPgb.PgbouncerConfig = mkStringMap(t, map[string]string{"pool_mode": "transaction"})
	body, ok, diags = postgresConfigPatchBody(ctx, &planPgb, &statePgb)
	if diags.HasError() || !ok {
		t.Fatalf("pgb-only: ok=%v diags=%+v", ok, diags)
	}
	keys = configPatchKeys(t, body)
	if _, has := keys["pgbouncer_config"]; !has {
		t.Errorf("pgb-only body must carry pgbouncer_config: %v", keys)
	}
	if _, has := keys["pg_config"]; has {
		t.Errorf("pgb-only body must OMIT pg_config (merge preserves it): %v", keys)
	}

	// both changed -> body carries both.
	var stateBoth, planBoth resource_postgres.PostgresModel
	stateBoth = withPg(stateBoth, map[string]string{"max_connections": "100"})
	planBoth = withPg(planBoth, map[string]string{"max_connections": "200"})
	stateBoth.PgbouncerConfig = mkStringMap(t, map[string]string{"pool_mode": "session"})
	planBoth.PgbouncerConfig = mkStringMap(t, map[string]string{"pool_mode": "transaction"})
	body, ok, diags = postgresConfigPatchBody(ctx, &planBoth, &stateBoth)
	if diags.HasError() || !ok {
		t.Fatalf("both: ok=%v diags=%+v", ok, diags)
	}
	keys = configPatchKeys(t, body)
	if _, has := keys["pg_config"]; !has {
		t.Errorf("both body must carry pg_config: %v", keys)
	}
	if _, has := keys["pgbouncer_config"]; !has {
		t.Errorf("both body must carry pgbouncer_config: %v", keys)
	}

	// neither changed -> ok=false (no config dispatch).
	if _, ok, _ := postgresConfigPatchBody(ctx, &statePg, &statePg); ok {
		t.Error("ok=true with no config change, want false")
	}
}
