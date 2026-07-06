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
