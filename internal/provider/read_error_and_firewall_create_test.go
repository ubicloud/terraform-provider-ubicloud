package provider

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-log/tflogtest"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_firewall_rule"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"
)

func statusServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// offlineClient wires the real generated client to srv, never a stub.
func offlineClient(t *testing.T, srv *httptest.Server) *UbicloudClient {
	t.Helper()
	client, err := ubicloud_client.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithResponses: %v", err)
	}
	return &UbicloudClient{client: client}
}

func mkRawFromSchema(objType tftypes.Object, overrides map[string]tftypes.Value) tftypes.Value {
	vals := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for name, typ := range objType.AttributeTypes {
		if v, ok := overrides[name]; ok {
			vals[name] = v
		} else {
			vals[name] = tftypes.NewValue(typ, nil)
		}
	}
	return tftypes.NewValue(objType, vals)
}

func resourceSchemaAndType(t *testing.T, ctx context.Context, r resource.Resource) (rschema.Schema, tftypes.Object) {
	t.Helper()
	var sresp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sresp)
	objType, ok := sresp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatalf("schema terraform type is not tftypes.Object")
	}
	return sresp.Schema, objType
}

func driveResourceRead(t *testing.T, ctx context.Context, r resource.Resource, overrides map[string]tftypes.Value) (*resource.ReadResponse, tftypes.Value) {
	t.Helper()
	schema, objType := resourceSchemaAndType(t, ctx, r)
	raw := mkRawFromSchema(objType, overrides)
	req := resource.ReadRequest{State: tfsdk.State{Schema: schema, Raw: raw}}
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: schema, Raw: raw}}
	r.Read(ctx, req, resp)
	return resp, raw
}

func driveResourceCreate(t *testing.T, ctx context.Context, r resource.Resource, overrides map[string]tftypes.Value) *resource.CreateResponse {
	t.Helper()
	schema, objType := resourceSchemaAndType(t, ctx, r)
	raw := mkRawFromSchema(objType, overrides)
	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: raw}}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schema, Raw: raw}}
	r.Create(ctx, req, resp)
	return resp
}

func strVal(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }

func setResourceClient(r resource.Resource, uc *UbicloudClient) {
	switch res := r.(type) {
	case *vmResource:
		res.uc = uc
	case *firewallResource:
		res.uc = uc
	case *firewallRuleResource:
		res.uc = uc
	case *privateSubnetResource:
		res.uc = uc
	case *projectResource:
		res.uc = uc
	default:
		panic(fmt.Sprintf("setResourceClient: unhandled resource type %T", r))
	}
}

// A non-404 error must surface a diagnostic AND leave state untouched (no RemoveResource);
// the branch is duplicated per resource (not shared), so each of the five is exercised.
func TestReadNonNotFoundErrorsAndPreservesState(t *testing.T) {
	ctx := t.Context()
	ids := map[string]tftypes.Value{
		"project_id":         strVal("pjx"),
		"location":           strVal("aws-us-east-1"),
		"name":               strVal("tf-acc-drift"),
		"id":                 strVal("idx"),
		"firewall_reference": strVal("tf-acc-fw"),
	}
	cases := []struct {
		name    string
		res     resource.Resource
		summary string
	}{
		{"vm", &vmResource{}, "Unexpected HTTP status code reading vm"},
		{"firewall", &firewallResource{}, "Unexpected HTTP status code reading firewall"},
		{"firewall_rule", &firewallRuleResource{}, "Unexpected HTTP status code reading firewall rule"},
		{"private_subnet", &privateSubnetResource{}, "Unexpected HTTP status code reading private subnet"},
		{"project", &projectResource{}, "Unexpected HTTP status code reading project"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := statusServer(t, http.StatusInternalServerError, `{"error":{"code":500,"message":"boom"}}`)
			setResourceClient(c.res, offlineClient(t, srv))
			_, objType := resourceSchemaAndType(t, ctx, c.res)
			over := map[string]tftypes.Value{}
			for k, v := range ids {
				if _, ok := objType.AttributeTypes[k]; ok {
					over[k] = v
				}
			}
			resp, inRaw := driveResourceRead(t, ctx, c.res, over)
			if !resp.Diagnostics.HasError() {
				t.Fatal("Read on a 500 must error, got success")
			}
			if got := resp.Diagnostics.Errors()[0].Summary(); got != c.summary {
				t.Fatalf("summary = %q, want %q", got, c.summary)
			}
			if !resp.State.Raw.Equal(inRaw) {
				t.Fatalf("Read on a non-404 error must leave state untouched; state changed from %v to %v", inRaw, resp.State.Raw)
			}
		})
	}
}

func TestReadPostgresNonNotFoundErrorsAndPreservesState(t *testing.T) {
	ctx := t.Context()
	srv := statusServer(t, http.StatusInternalServerError, `{"error":{"code":500,"message":"boom"}}`)
	r := newTestPostgresResource(t, srv)
	resp := driveRead(t, ctx, r, nil)
	if !resp.Diagnostics.HasError() {
		t.Fatal("Read on a 500 must error, got success")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); got != "Unexpected HTTP status code reading postgres database" {
		t.Fatalf("summary = %q, want Unexpected HTTP status code reading postgres database", got)
	}
	// driveRead seeds state from mkPGRaw(nil); the same deterministic value is the untouched
	// expectation, so this catches an overwrite-to-different-non-null, not just a drop.
	if !resp.State.Raw.Equal(mkPGRaw(t, ctx, nil)) {
		t.Fatal("Read on a non-404 error must leave state untouched (no RemoveResource, no overwrite)")
	}
}

func TestReadFirewallNotFoundEmitsDebugLog(t *testing.T) {
	var buf bytes.Buffer
	ctx := tflogtest.RootLogger(t.Context(), &buf)
	srv := statusServer(t, http.StatusNotFound, `{"error":{"code":404,"message":"not found"}}`)
	r := &firewallResource{uc: offlineClient(t, srv)}
	resp, _ := driveResourceRead(t, ctx, r, map[string]tftypes.Value{
		"project_id": strVal("pjx"),
		"location":   strVal("aws-us-east-1"),
		"name":       strVal("tf-acc-fw"),
		"id":         strVal("idx"),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("Read on a 404 must not error: %+v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Fatal("Read on a 404 must RemoveResource (null state)")
	}

	entries, err := tflogtest.MultilineJSONDecode(&buf)
	if err != nil {
		t.Fatalf("decoding log output: %v", err)
	}
	found := false
	for _, e := range entries {
		if e["@message"] != "Firewall not found, removing from state: project_id=pjx, location=aws-us-east-1, name=tf-acc-fw" {
			continue
		}
		if e["@level"] != "debug" {
			t.Fatalf("removing-from-state log level = %v, want debug", e["@level"])
		}
		found = true
	}
	if !found {
		t.Fatalf("no Debug 'Firewall not found, removing from state' entry with the identifier; entries=%+v", entries)
	}
}

// firewallRuleArrayServer returns a 200 whose body is the array form of the create response
// (the FirewallRuleOrRules anyOf []FirewallRule member), with n synthetic rules.
func firewallRuleArrayServer(t *testing.T, n int) *httptest.Server {
	t.Helper()
	rules := "["
	for i := 0; i < n; i++ {
		if i > 0 {
			rules += ","
		}
		rules += fmt.Sprintf(`{"id":"fr%d","cidr":"1.2.3.0/24","description":"d","port_range":"5432..5432","protocol":"tcp"}`, i)
	}
	rules += "]"
	return statusServer(t, http.StatusOK, rules)
}

// The array form (the private-subnet fan-out shape) cannot be tracked by a single
// firewall_rule resource; AsFirewallRule fails to unmarshal it, so Create refuses and sets no state.
func TestCreateFirewallRuleRejectsMultiRuleResult(t *testing.T) {
	ctx := t.Context()
	srv := firewallRuleArrayServer(t, 2)
	r := &firewallRuleResource{uc: offlineClient(t, srv)}
	resp := driveResourceCreate(t, ctx, r, map[string]tftypes.Value{
		"project_id":         strVal("pjx"),
		"location":           strVal("aws-us-east-1"),
		"firewall_reference": strVal("tf-acc-fw"),
		"cidr":               strVal("1.2.3.0/24"),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("a multi-rule create result must error, got success")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); got != "Error parsing firewall rule response" {
		t.Fatalf("summary = %q, want Error parsing firewall rule response", got)
	}
	// Create refuses before mapping, so id keeps the null plan value.
	var state resource_firewall_rule.FirewallRuleModel
	diags := resp.State.Get(ctx, &state)
	if diags.HasError() {
		t.Fatalf("reading state: %+v", diags)
	}
	if !state.Id.IsNull() {
		t.Fatalf("a refused multi-rule create must not map a rule into state; id = %q", state.Id.ValueString())
	}
}

func TestCreateFirewallRuleMapsObjectResult(t *testing.T) {
	ctx := t.Context()
	srv := statusServer(t, http.StatusOK, `{"id":"fr0","cidr":"1.2.3.0/24","description":"d","port_range":"5432..5432","protocol":"tcp"}`)
	r := &firewallRuleResource{uc: offlineClient(t, srv)}
	resp := driveResourceCreate(t, ctx, r, map[string]tftypes.Value{
		"project_id":         strVal("pjx"),
		"location":           strVal("aws-us-east-1"),
		"firewall_reference": strVal("tf-acc-fw"),
		"cidr":               strVal("1.2.3.0/24"),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("a bare-object create must succeed: %+v", resp.Diagnostics)
	}
	var state resource_firewall_rule.FirewallRuleModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		t.Fatalf("reading state: %+v", resp.Diagnostics)
	}
	if state.Id.ValueString() != "fr0" {
		t.Fatalf("state id = %q, want fr0 mapped from the response", state.Id.ValueString())
	}
	if state.Cidr.ValueString() != "1.2.3.0/24" {
		t.Fatalf("state cidr = %q, want 1.2.3.0/24 from the response", state.Cidr.ValueString())
	}
}
