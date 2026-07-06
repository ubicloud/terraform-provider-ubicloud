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

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
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

// captureRT records every request and returns canned, schema-valid responses the real
// generated client parses; the zero value serves the happy path.
type captureRT struct {
	reqs            []capturedReq
	detailGetStatus int
	// detailGetNonJSON answers the trailing detail GET with a 200 text/html proxy/LB error
	// page, so the client parses JSON200 to nil with no transport error.
	detailGetNonJSON bool
	// configStatus fails only the GET .../config read-back; configPatchStatus covers the PATCH.
	configStatus int

	configBody *ubicloud_client.PostgresConfig

	detailBody *ubicloud_client.PostgresDatabase
	// detailState overrides the sample's state. Create blocks until the detail GET reports
	// running; the default sample stays creating, keeping update-against-creating coverage.
	detailState string

	upgradeStatus string

	upgradeStage string
	// upgradeStatusCode overrides the /upgrade HTTP status (400 = "Database is not
	// upgrading" = the upgrade already converged).
	upgradeStatusCode int

	upgradeTransportErr bool
	// Each status/transportErr pair below fails one mutation endpoint, method+suffix scoped
	// so the trailing detail GET still succeeds.
	patchStatus       int
	patchTransportErr bool

	renameStatus       int
	renameTransportErr bool

	configPatchStatus       int
	configPatchTransportErr bool

	setMWStatus       int
	setMWTransportErr bool
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
	if c.renameTransportErr && req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/rename") {
		return nil, errors.New("simulated rename transport error")
	}
	if c.configPatchTransportErr && req.Method == http.MethodPatch && strings.HasSuffix(req.URL.Path, "/config") {
		return nil, errors.New("simulated config patch transport error")
	}
	if c.patchTransportErr && req.Method == http.MethodPatch && !strings.HasSuffix(req.URL.Path, "/config") {
		return nil, errors.New("simulated patch transport error")
	}
	if c.setMWTransportErr && req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/set-maintenance-window") {
		return nil, errors.New("simulated set-maintenance-window transport error")
	}

	status := http.StatusOK
	contentType := "application/json"
	var respBody []byte
	switch {
	case strings.HasSuffix(req.URL.Path, "/config"):
		switch {
		case req.Method == http.MethodGet && c.configStatus != 0:
			status = c.configStatus
			respBody = specErrorBody(status, "simulated config read failure")
		case req.Method == http.MethodPatch && c.configPatchStatus != 0:
			status = c.configPatchStatus
			respBody = specErrorBody(status, "simulated config patch failure")
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
		upgradeBody := ubicloud_client.PostgresDatabaseUpgradeStatus{
			CurrentVersion: ubicloud_client.PostgresDatabaseUpgradeStatusCurrentVersionN16,
			TargetVersion:  ubicloud_client.PostgresDatabaseUpgradeStatusTargetVersionN17,
			UpgradeStatus:  upgradeStatus,
		}
		if c.upgradeStage != "" {
			upgradeBody.UpgradeStage = &c.upgradeStage
		}
		respBody, _ = json.Marshal(upgradeBody)
	case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/rename"):
		if c.renameStatus != 0 {
			status = c.renameStatus
			respBody = specErrorBody(status, "simulated rename failure")
		} else {
			// The caller only checks status, but the client still parses the 200 body, so echo
			// a schema-valid detail carrying the addressed name.
			sample := sampleDetailedPostgresResponse()
			sample.Name = pgNameFromPath(req.URL.Path)
			respBody, _ = json.Marshal(sample)
		}
	case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/set-maintenance-window"):
		if c.setMWStatus != 0 {
			status = c.setMWStatus
			respBody = specErrorBody(status, "simulated set-maintenance-window failure")
		} else {
			sample := sampleDetailedPostgresResponse()
			sample.Name = pgNameFromPath(req.URL.Path)
			respBody, _ = json.Marshal(sample)
		}
	default:
		switch {
		case req.Method == http.MethodPatch && c.patchStatus != 0:
			status = c.patchStatus
			respBody = specErrorBody(status, "simulated patch failure")
		case req.Method == http.MethodGet && c.detailGetStatus != 0:
			status = c.detailGetStatus
			respBody = specErrorBody(status, "simulated read failure")
		case req.Method == http.MethodGet && c.detailGetNonJSON:
			contentType = "text/html"
			respBody = []byte("<html><body>502 Bad Gateway</body></html>")
		case c.detailBody != nil:
			respBody, _ = json.Marshal(*c.detailBody)
		default:
			// Echo the sample (creating by default; see detailState), with the name taken from
			// the path so a rename re-read reports the renamed row.
			sample := sampleDetailedPostgresResponse()
			sample.Name = pgNameFromPath(req.URL.Path)
			if c.detailState != "" {
				sample.State = c.detailState
			}
			respBody, _ = json.Marshal(sample)
		}
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(bytes.NewReader(respBody)),
		Request:    req,
	}, nil
}

// specErrorBody renders a spec-valid Error body (schemas/Error requires error.{code,message,type})
// so simulated failures parse like real clover errors.
func specErrorBody(code int, message string) []byte {
	errType := "InvalidRequest"
	switch {
	case code == http.StatusNotFound:
		errType = "NotFound"
	case code >= 500:
		errType = "InternalError"
	}
	b, _ := json.Marshal(map[string]any{
		"error": map[string]any{"code": code, "message": message, "type": errType},
	})
	return b
}

// dispatchReqs drops GETs (every successful Update re-reads), leaving only the mutation calls.
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

func newPostgresResourceWithRT(t *testing.T, rt http.RoundTripper) *postgresResource {
	t.Helper()
	hc := &http.Client{Transport: rt}
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

// postgresUnpinnedComputeds are the computed attributes with no UseStateForUnknown modifier;
// the framework marks each unknown on every update plan, so realistic plans carry them unknown.
var postgresUnpinnedComputeds = []string{
	"state", "vm_size", "storage_size_gib", "target_vm_size", "target_storage_size_gib",
	"target_version", "target_server_count", "connection_string", "hostname", "password",
	"earliest_restore_time", "latest_restore_time", "fallback_active", "firewall_rules",
}

// withUnknownComputeds reproduces the framework-realistic update plan (postgresRaw's all-null
// default would hide unknown-handling bugs).
func withUnknownComputeds(t *testing.T, ctx context.Context, over map[string]tftypes.Value) map[string]tftypes.Value {
	t.Helper()
	objType := postgresResourceSchemaObjType(t, ctx)
	out := make(map[string]tftypes.Value, len(over)+len(postgresUnpinnedComputeds))
	for k, v := range over {
		out[k] = v
	}
	for _, name := range postgresUnpinnedComputeds {
		if _, ok := out[name]; ok {
			continue
		}
		typ, ok := objType.AttributeTypes[name]
		if !ok {
			t.Fatalf("unpinned computed %q absent from schema", name)
		}
		out[name] = tftypes.NewValue(typ, tftypes.UnknownValue)
	}
	return out
}

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
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if out.Name.ValueString() != "tf-acc-pg-renamed" {
		t.Errorf("state name = %q, want the renamed tf-acc-pg-renamed", out.Name.ValueString())
	}
}

// The NEW name must persist even when the post-rename re-read fails, so a later refresh
// addresses the renamed row instead of 404-ing into a spurious recreate.
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

// The config PATCH is a merge: an omitted map is left untouched server-side, where a full
// replace would send {} and wipe live config, so unchanged pgbouncer_config must be omitted.
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

// The upgrade endpoint advances exactly one major, server-chosen, so the POST carries no
// body; version is not a PATCH field.
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

// The backend rejects a second POST .../upgrade until convergence, so an in-flight upgrade
// is reconciled by querying GET .../upgrade and holding the planned version.
func TestUpdateVersionInFlightSkipsUpgrade(t *testing.T) {
	ctx := t.Context()
	// The lagging detailBody (16 with target 17) makes the version assertion load-bearing:
	// the converged default sample would report 17 regardless.
	lagging := sampleDetailedPostgresResponse()
	lagging.Name = "tf-acc-pg"
	lagging.Version = ubicloud_client.PostgresDatabaseVersionN16
	lagging.TargetVersion = ubicloud_client.PostgresDatabaseTargetVersionN17
	capRT := &captureRT{detailBody: &lagging}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
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

func TestUpdateVersionInFlightFailedRendersStage(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{upgradeStatus: "failed", upgradeStage: "prepare_standby"}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{"version": strRaw("16"), "target_version": strRaw("17")},
		map[string]tftypes.Value{"version": strRaw("17")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed in-flight upgrade must surface an error")
	}
	if detail := resp.Diagnostics.Errors()[0].Detail(); !strings.Contains(detail, `stage "prepare_standby"`) {
		t.Fatalf("detail = %q, want the failed upgrade stage rendered", detail)
	}
}

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

// A major upgrade is irreversible: dispatching it alongside another mutation risks erroring
// after the upgrade landed, stranding a half-apply, so it must arrive alone.
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

// The re-read reports the lagging version until convergence; post-apply state must carry
// the REQUESTED version or the framework raises an inconsistent-result error.
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

// driveModifyPlan runs ModifyPlan on the update path (non-null prior state).
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

// Even a no-mutable-change Update (e.g. timeouts-only) arrives with the unpinned computeds
// unknown; persisting the plan verbatim leaves state unknown, which Terraform rejects.
func TestUpdateNoMutableChangeReReadsResolvesComputeds(t *testing.T) {
	ctx := t.Context()
	stable := map[string]tftypes.Value{
		"size":         strRaw("m8gd.large"),
		"storage_size": numRaw(118),
		"tags":         rawTags(t, ctx, [][2]string{{"team", "data"}}),
	}
	plan := withUnknownComputeds(t, ctx, map[string]tftypes.Value{
		"size":         strRaw("m8gd.large"),
		"storage_size": numRaw(118),
		"tags":         rawTags(t, ctx, [][2]string{{"team", "data"}}),
	})
	capRT, resp := runUpdate(t, ctx, stable, plan)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	if got := capRT.dispatchReqs(); len(got) != 0 {
		t.Errorf("no-mutable-change Update must issue no mutations, got %+v", got)
	}
	if len(capRT.reqs) != 1 || capRT.reqs[0].Method != http.MethodGet {
		t.Errorf("want exactly one resolving detail GET, got %+v", capRT.reqs)
	}
	if !resp.State.Raw.IsFullyKnown() {
		t.Errorf("state has unknown values after no-mutation apply: %v", resp.State.Raw)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if got := out.Password.ValueString(); got != "supersecret" {
		t.Errorf("password = %q, want hydrated supersecret", got)
	}
	if got := out.ConnectionString.ValueString(); got != "postgres://postgres:supersecret@:5432/postgres" {
		t.Errorf("connection_string = %q, want hydrated value", got)
	}
	if got := out.State.ValueString(); got != "creating" {
		t.Errorf("state = %q, want hydrated creating", got)
	}
}

// Unmanaged (null) tags must stay null through the resolving re-read: adopting server tags
// re-arms the phantom update and mismatches the planned null.
func TestUpdateNoMutableChangeTagsUnmanagedStaysNull(t *testing.T) {
	ctx := t.Context()
	stable := map[string]tftypes.Value{
		"size":         strRaw("m8gd.large"),
		"storage_size": numRaw(118),
	}
	plan := withUnknownComputeds(t, ctx, map[string]tftypes.Value{
		"size":         strRaw("m8gd.large"),
		"storage_size": numRaw(118),
	})
	capRT, resp := runUpdate(t, ctx, stable, plan)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	if got := capRT.dispatchReqs(); len(got) != 0 {
		t.Errorf("no-mutable-change Update must issue no mutations, got %+v", got)
	}
	if !resp.State.Raw.IsFullyKnown() {
		t.Errorf("state has unknown values after no-mutation apply: %v", resp.State.Raw)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if !out.Tags.IsNull() {
		t.Errorf("tags = %v, want null (server tags must not be adopted)", out.Tags)
	}
}

func TestUpdateNoMutableChangeReadFailsClosed(t *testing.T) {
	ctx := t.Context()
	stable := map[string]tftypes.Value{
		"size":         strRaw("m8gd.large"),
		"storage_size": numRaw(118),
		"tags":         rawTags(t, ctx, [][2]string{{"team", "data"}}),
	}
	plan := withUnknownComputeds(t, ctx, map[string]tftypes.Value{
		"size":         strRaw("m8gd.large"),
		"storage_size": numRaw(118),
		"tags":         rawTags(t, ctx, [][2]string{{"team", "data"}}),
	})
	capRT := &captureRT{detailGetStatus: http.StatusInternalServerError}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r, stable, plan)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed post-update re-read must surface an error, not persist unknowns")
	}
	if got := capRT.dispatchReqs(); len(got) != 0 {
		t.Errorf("no mutation must fire on the no-mutable-change path, got %+v", got)
	}
}

func TestUpdateSizeReadbackKeepsRequestedSize(t *testing.T) {
	ctx := t.Context()
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

func TestUpdateStorageSizeReadbackKeepsRequestedSize(t *testing.T) {
	ctx := t.Context()
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{"storage_size": numRaw(118)},
		map[string]tftypes.Value{"storage_size": numRaw(256)},
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
	if got := out.StorageSize.ValueInt64(); got != 256 {
		t.Errorf("state storage_size = %d, want requested 256 (not the lagging actual)", got)
	}
}

// A restore/read-replica plan leaves storage_size unknown; the re-pin guard must let the
// read-back stand (prior 64 vs read-back 118 shows which won), never pin the unknown.
func TestUpdateStorageSizeUnknownKeepsReadback(t *testing.T) {
	ctx := t.Context()
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{"size": strRaw("m8gd.large"), "storage_size": numRaw(64)},
		map[string]tftypes.Value{"size": strRaw("m8gd.xlarge"), "storage_size": tftypes.NewValue(tftypes.Number, tftypes.UnknownValue)},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	if len(capRT.dispatchReqs()) != 1 {
		t.Fatalf("want one PATCH for the size change, got %+v", capRT.dispatchReqs())
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if out.StorageSize.IsUnknown() {
		t.Fatal("storage_size is unknown: the guard re-pinned the unknown plan value instead of keeping the read-back")
	}
	if got := out.StorageSize.ValueInt64(); got != 118 {
		t.Errorf("state storage_size = %d, want read-back 118 (guard skips re-pin when plan is unknown)", got)
	}
}

func TestPostgresPatchBodyOnlyChangedFields(t *testing.T) {
	ctx := t.Context()
	var state resource_postgres.PostgresModel
	state.Size = types.StringValue("m8gd.large")
	state.StorageSize = types.Int64Value(118)
	state.HaType = types.StringValue("none")
	state.Tags = mkResourceTags(t, [][2]string{{"team", "data"}})

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

	if _, ok, _ := postgresPatchBody(ctx, &state, &state); ok {
		t.Error("ok = true with no PATCH-set change, want false")
	}
}

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

func TestPostgresConfigPatchBodyOnlyChangedMaps(t *testing.T) {
	ctx := t.Context()
	withPg := func(m resource_postgres.PostgresModel, kv map[string]string) resource_postgres.PostgresModel {
		m.PgConfig = mkStringMap(t, kv)
		return m
	}

	var statePg, planPg resource_postgres.PostgresModel
	statePg = withPg(statePg, map[string]string{"max_connections": "100"})
	planPg = withPg(planPg, map[string]string{"max_connections": "200"})
	statePg.PgbouncerConfig = types.MapNull(types.StringType)
	planPg.PgbouncerConfig = types.MapNull(types.StringType)
	body, diags := postgresConfigPatchBody(ctx, &planPg, &statePg, nil)
	if diags.HasError() {
		t.Fatalf("pg-only: diags=%+v", diags)
	}
	keys := configPatchKeys(t, body)
	if _, has := keys["pg_config"]; !has {
		t.Errorf("pg-only body must carry pg_config: %v", keys)
	}
	if _, has := keys["pgbouncer_config"]; has {
		t.Errorf("pg-only body must OMIT pgbouncer_config (merge preserves it): %v", keys)
	}

	var statePgb, planPgb resource_postgres.PostgresModel
	statePgb.PgConfig = types.MapNull(types.StringType)
	planPgb.PgConfig = types.MapNull(types.StringType)
	statePgb.PgbouncerConfig = mkStringMap(t, map[string]string{"pool_mode": "session"})
	planPgb.PgbouncerConfig = mkStringMap(t, map[string]string{"pool_mode": "transaction"})
	body, diags = postgresConfigPatchBody(ctx, &planPgb, &statePgb, nil)
	if diags.HasError() {
		t.Fatalf("pgb-only: diags=%+v", diags)
	}
	keys = configPatchKeys(t, body)
	if _, has := keys["pgbouncer_config"]; !has {
		t.Errorf("pgb-only body must carry pgbouncer_config: %v", keys)
	}
	if _, has := keys["pg_config"]; has {
		t.Errorf("pgb-only body must OMIT pg_config (merge preserves it): %v", keys)
	}

	var stateBoth, planBoth resource_postgres.PostgresModel
	stateBoth = withPg(stateBoth, map[string]string{"max_connections": "100"})
	planBoth = withPg(planBoth, map[string]string{"max_connections": "200"})
	stateBoth.PgbouncerConfig = mkStringMap(t, map[string]string{"pool_mode": "session"})
	planBoth.PgbouncerConfig = mkStringMap(t, map[string]string{"pool_mode": "transaction"})
	body, diags = postgresConfigPatchBody(ctx, &planBoth, &stateBoth, nil)
	if diags.HasError() {
		t.Fatalf("both: diags=%+v", diags)
	}
	keys = configPatchKeys(t, body)
	if _, has := keys["pg_config"]; !has {
		t.Errorf("both body must carry pg_config: %v", keys)
	}
	if _, has := keys["pgbouncer_config"]; !has {
		t.Errorf("both body must carry pgbouncer_config: %v", keys)
	}

}

func diagHasSummaryOnPath(diags diag.Diagnostics, summary string, p path.Path) bool {
	for _, d := range diags.Errors() {
		if d.Summary() != summary {
			continue
		}
		wp, ok := d.(diag.DiagnosticWithPath)
		if ok && wp.Path().Equal(p) {
			return true
		}
	}
	return false
}

func diagHasSummary(diags diag.Diagnostics, summary string) bool {
	for _, d := range diags.Errors() {
		if d.Summary() == summary {
			return true
		}
	}
	return false
}

// Exercises postgresVersionUpgradeError's strconv.Atoi guard, unreachable via clean inputs.
func TestUpdateVersionNonNumericRejected(t *testing.T) {
	ctx := t.Context()
	cases := []struct {
		name              string
		stateVer, planVer string
	}{
		{"plan non-numeric", "16", "abc"},
		{"state non-numeric", "abc", "16"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capRT, resp := runUpdate(t, ctx,
				map[string]tftypes.Value{"version": strRaw(tc.stateVer)},
				map[string]tftypes.Value{"version": strRaw(tc.planVer)},
			)
			if !resp.Diagnostics.HasError() {
				t.Fatalf("non-numeric version (%q->%q) must error", tc.stateVer, tc.planVer)
			}
			if !diagHasSummary(resp.Diagnostics, "Invalid postgres version") {
				t.Errorf("want \"Invalid postgres version\" summary, got %+v", resp.Diagnostics)
			}
			if len(capRT.reqs) != 0 {
				t.Errorf("an uninterpretable version must issue no REST calls, got %+v", capRT.reqs)
			}
		})
	}
}

func TestModifyPlanVersionDowngradeRejected(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"version": strRaw("17")},
		map[string]tftypes.Value{"version": strRaw("16")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("version downgrade must be rejected at plan time")
	}
	if !diagHasSummaryOnPath(resp.Diagnostics, "Unsupported postgres version change", path.Root("version")) {
		t.Errorf("downgrade rejection must be attribute-scoped to version, got %+v", resp.Diagnostics)
	}
}

// Each companion runs alone with the exact summary asserted, so a dropped OR-term in
// postgresNonVersionMutableChanged fails its own case.
func TestUpdateVersionWithEachOtherChangeRejected(t *testing.T) {
	ctx := t.Context()
	const summary = "Postgres version upgrade must be applied on its own"
	cases := []struct {
		name             string
		stateOver, plan2 map[string]tftypes.Value
	}{
		{"storage_size", map[string]tftypes.Value{"storage_size": numRaw(118)}, map[string]tftypes.Value{"storage_size": numRaw(256)}},
		{"ha_type", map[string]tftypes.Value{"ha_type": strRaw("none")}, map[string]tftypes.Value{"ha_type": strRaw("sync")}},
		{"tags", map[string]tftypes.Value{"tags": rawTags(t, ctx, [][2]string{{"team", "data"}})}, map[string]tftypes.Value{"tags": rawTags(t, ctx, [][2]string{{"team", "data"}, {"env", "prod"}})}},
		{"pg_config", map[string]tftypes.Value{"pg_config": rawConfigMap(map[string]string{"max_connections": "100"})}, map[string]tftypes.Value{"pg_config": rawConfigMap(map[string]string{"max_connections": "200"})}},
		{"pgbouncer_config", map[string]tftypes.Value{"pgbouncer_config": rawConfigMap(map[string]string{"pool_mode": "session"})}, map[string]tftypes.Value{"pgbouncer_config": rawConfigMap(map[string]string{"pool_mode": "transaction"})}},
		{"name", map[string]tftypes.Value{}, map[string]tftypes.Value{"name": strRaw("tf-acc-pg-renamed")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stateOver := map[string]tftypes.Value{"version": strRaw("16")}
			for k, v := range tc.stateOver {
				stateOver[k] = v
			}
			planOver := map[string]tftypes.Value{"version": strRaw("17")}
			for k, v := range tc.plan2 {
				planOver[k] = v
			}
			capRT, resp := runUpdate(t, ctx, stateOver, planOver)
			if !resp.Diagnostics.HasError() {
				t.Fatalf("version + %s must be rejected", tc.name)
			}
			if !diagHasSummary(resp.Diagnostics, summary) {
				t.Errorf("version + %s must raise %q, got %+v", tc.name, summary, resp.Diagnostics)
			}
			if len(capRT.reqs) != 0 {
				t.Errorf("a rejected combined change must issue no REST calls, got %+v", capRT.reqs)
			}
		})
	}
}

func TestUpdateDispatchHaTypeOnly(t *testing.T) {
	ctx := t.Context()
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{"ha_type": strRaw("none")},
		map[string]tftypes.Value{"ha_type": strRaw("sync")},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	d := capRT.dispatchReqs()
	if len(d) != 1 || d[0].Method != http.MethodPatch {
		t.Fatalf("dispatch = %+v, want one PATCH", d)
	}
	b := bodyJSON(t, d[0].Body)
	if b["ha_type"] != "sync" {
		t.Errorf("PATCH ha_type = %v, want sync: %s", b["ha_type"], d[0].Body)
	}
	for _, k := range []string{"size", "storage_size", "tags"} {
		if _, ok := b[k]; ok {
			t.Errorf("PATCH body must omit unchanged %q: %s", k, d[0].Body)
		}
	}
}

// Transport-error summaries embed a dynamic log identifier, hence prefix matching.
func diagHasSummaryPrefix(diags diag.Diagnostics, prefix string) bool {
	for _, d := range diags.Errors() {
		if strings.HasPrefix(d.Summary(), prefix) {
			return true
		}
	}
	return false
}

// sawDetailReread reports whether the trailing detail re-read fired; GET .../config and
// .../upgrade are pre-dispatch reads, not the re-read.
func (c *captureRT) sawDetailReread() bool {
	for _, r := range c.reqs {
		if r.Method == http.MethodGet &&
			!strings.HasSuffix(r.Path, "/config") &&
			!strings.HasSuffix(r.Path, "/upgrade") {
			return true
		}
	}
	return false
}

func TestUpdateApplyPatchNon200(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{patchStatus: http.StatusInternalServerError}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{"size": strRaw("m8gd.large")},
		map[string]tftypes.Value{"size": strRaw("m8gd.xlarge")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a non-200 PATCH must surface an error")
	}
	if !diagHasSummary(resp.Diagnostics, "Unexpected HTTP status code updating postgres database") {
		t.Errorf("want the PATCH status summary, got %+v", resp.Diagnostics)
	}
	if capRT.sawDetailReread() {
		t.Errorf("a failed PATCH must return before the detail re-read, got %+v", capRT.reqs)
	}
}

func TestUpdateApplyPatchTransportError(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{patchTransportErr: true}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{"size": strRaw("m8gd.large")},
		map[string]tftypes.Value{"size": strRaw("m8gd.xlarge")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a PATCH transport error must surface an error")
	}
	if !diagHasSummaryPrefix(resp.Diagnostics, "Error updating postgres database") {
		t.Errorf("want the PATCH transport-error summary, got %+v", resp.Diagnostics)
	}
	if capRT.sawDetailReread() {
		t.Errorf("a failed PATCH must return before the detail re-read, got %+v", capRT.reqs)
	}
}

func TestUpdateApplyRenameNon200(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{renameStatus: http.StatusInternalServerError}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{},
		map[string]tftypes.Value{"name": strRaw("tf-acc-pg-renamed")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a non-200 rename must surface an error")
	}
	if !diagHasSummary(resp.Diagnostics, "Unexpected HTTP status code renaming postgres database") {
		t.Errorf("want the rename status summary, got %+v", resp.Diagnostics)
	}
	if capRT.sawDetailReread() {
		t.Errorf("a failed rename must return before the detail re-read, got %+v", capRT.reqs)
	}
}

func TestUpdateApplyRenameTransportError(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{renameTransportErr: true}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{},
		map[string]tftypes.Value{"name": strRaw("tf-acc-pg-renamed")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a rename transport error must surface an error")
	}
	if !diagHasSummaryPrefix(resp.Diagnostics, "Error renaming postgres database") {
		t.Errorf("want the rename transport-error summary, got %+v", resp.Diagnostics)
	}
	if capRT.sawDetailReread() {
		t.Errorf("a failed rename must return before the detail re-read, got %+v", capRT.reqs)
	}
}

func TestUpdateApplyUpgradeNon200(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{upgradeStatusCode: http.StatusInternalServerError}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{"version": strRaw("16")},
		map[string]tftypes.Value{"version": strRaw("17")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a non-200 upgrade must surface an error")
	}
	if !diagHasSummary(resp.Diagnostics, "Unexpected HTTP status code upgrading postgres database") {
		t.Errorf("want the upgrade status summary, got %+v", resp.Diagnostics)
	}
	if capRT.sawDetailReread() {
		t.Errorf("a failed upgrade must return before any re-read, got %+v", capRT.reqs)
	}
}

func TestUpdateApplyUpgradeTransportError(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{upgradeTransportErr: true}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{"version": strRaw("16")},
		map[string]tftypes.Value{"version": strRaw("17")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("an upgrade transport error must surface an error")
	}
	if !diagHasSummaryPrefix(resp.Diagnostics, "Error upgrading postgres database") {
		t.Errorf("want the upgrade transport-error summary, got %+v", resp.Diagnostics)
	}
	if capRT.sawDetailReread() {
		t.Errorf("a failed upgrade must return before any re-read, got %+v", capRT.reqs)
	}
}

func TestApplyPostgresConfigMergeSurfacesErrors(t *testing.T) {
	ctx := t.Context()
	var body ubicloud_client.PatchPostgresDatabaseConfigJSONRequestBody
	pg := "200"
	if err := body.FromPostgresPatchConfig0(ubicloud_client.PostgresPatchConfig0{PgConfig: map[string]*string{"max_connections": &pg}}); err != nil {
		t.Fatalf("build config body: %v", err)
	}

	t.Run("non-200", func(t *testing.T) {
		capRT := &captureRT{configPatchStatus: http.StatusInternalServerError}
		r := newPostgresResourceWithRT(t, capRT)
		diags := r.applyPostgresConfigMerge(ctx, "pjtest", "aws-us-east-1", "tf-acc-pg", body, "logid")
		if !diags.HasError() {
			t.Fatal("a non-200 config merge must surface an error")
		}
		if !diagHasSummary(diags, "Unexpected HTTP status code updating postgres database config") {
			t.Errorf("want the config status summary, got %+v", diags)
		}
	})

	t.Run("transport error", func(t *testing.T) {
		capRT := &captureRT{configPatchTransportErr: true}
		r := newPostgresResourceWithRT(t, capRT)
		diags := r.applyPostgresConfigMerge(ctx, "pjtest", "aws-us-east-1", "tf-acc-pg", body, "logid")
		if !diags.HasError() {
			t.Fatal("a config merge transport error must surface an error")
		}
		if !diagHasSummaryPrefix(diags, "Error updating postgres database config") {
			t.Errorf("want the config transport-error summary, got %+v", diags)
		}
	})
}

func TestUpdateApplyConfigMergeNon200(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{configPatchStatus: http.StatusInternalServerError}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{"pg_config": rawConfigMap(map[string]string{"max_connections": "100"})},
		map[string]tftypes.Value{"pg_config": rawConfigMap(map[string]string{"max_connections": "200"})},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a non-200 config merge must surface an error")
	}
	if !diagHasSummary(resp.Diagnostics, "Unexpected HTTP status code updating postgres database config") {
		t.Errorf("want the config status summary, got %+v", resp.Diagnostics)
	}
	if capRT.sawDetailReread() {
		t.Errorf("a failed config merge must return before the detail re-read, got %+v", capRT.reqs)
	}
}

func TestUpdateApplyConfigMergeTransportError(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{configPatchTransportErr: true}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{"pg_config": rawConfigMap(map[string]string{"max_connections": "100"})},
		map[string]tftypes.Value{"pg_config": rawConfigMap(map[string]string{"max_connections": "200"})},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a config merge transport error must surface an error")
	}
	if !diagHasSummaryPrefix(resp.Diagnostics, "Error updating postgres database config") {
		t.Errorf("want the config transport-error summary, got %+v", resp.Diagnostics)
	}
	if capRT.sawDetailReread() {
		t.Errorf("a failed config merge must return before the detail re-read, got %+v", capRT.reqs)
	}
}
