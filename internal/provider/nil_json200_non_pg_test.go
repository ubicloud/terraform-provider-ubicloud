package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// nonJSONOKServer answers 200 text/plain (a proxy/LB interposition body); the generated
// parsers fill JSON200 only when Content-Type contains "json", so err==nil, 200, JSON200==nil.
func nonJSONOKServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("502 Bad Gateway"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func setDatasourceClient(d datasource.DataSource, uc *UbicloudClient) {
	switch ds := d.(type) {
	case *vmDataSource:
		ds.uc = uc
	case *firewallDataSource:
		ds.uc = uc
	case *firewallRuleDataSource:
		ds.uc = uc
	case *privateSubnetDataSource:
		ds.uc = uc
	case *projectDataSource:
		ds.uc = uc
	default:
		panic(fmt.Sprintf("setDatasourceClient: unhandled datasource type %T", d))
	}
}

func datasourceObjType(t *testing.T, ctx context.Context, d datasource.DataSource) tftypes.Object {
	t.Helper()
	var sresp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &sresp)
	objType, ok := sresp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatalf("datasource schema terraform type is not tftypes.Object")
	}
	return objType
}

func driveGenericDatasourceRead(t *testing.T, ctx context.Context, d datasource.DataSource, overrides map[string]tftypes.Value) (*datasource.ReadResponse, tftypes.Value) {
	t.Helper()
	var sresp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &sresp)
	objType, ok := sresp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatalf("datasource schema terraform type is not tftypes.Object")
	}
	raw := mkRawFromSchema(objType, overrides)
	req := datasource.ReadRequest{Config: tfsdk.Config{Schema: sresp.Schema, Raw: raw}}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: sresp.Schema, Raw: raw}}
	d.Read(ctx, req, resp)
	return resp, raw
}

func restrictOverrides(objType tftypes.Object, ids map[string]tftypes.Value) map[string]tftypes.Value {
	over := map[string]tftypes.Value{}
	for k, v := range ids {
		if _, ok := objType.AttributeTypes[k]; ok {
			over[k] = v
		}
	}
	return over
}

// A 200 with a non-JSON body (JSON200 nil) must fail closed and leave state untouched:
// a nil body is not a 404, so no RemoveResource.
func TestReadFailsClosedOnEmptyBodyNonPg(t *testing.T) {
	ctx := t.Context()
	ids := map[string]tftypes.Value{
		"project_id":         strVal("pjx"),
		"location":           strVal("aws-us-east-1"),
		"name":               strVal("tf-acc-empty"),
		"id":                 strVal("idx"),
		"firewall_reference": strVal("tf-acc-fw"),
	}
	cases := []struct {
		name    string
		res     resource.Resource
		summary string
	}{
		{"vm", &vmResource{}, "Empty response reading vm"},
		{"firewall", &firewallResource{}, "Empty response reading firewall"},
		{"firewall_rule", &firewallRuleResource{}, "Empty response reading firewall rule"},
		{"private_subnet", &privateSubnetResource{}, "Empty response reading private subnet"},
		{"project", &projectResource{}, "Empty response reading project"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setResourceClient(c.res, offlineClient(t, nonJSONOKServer(t)))
			_, objType := resourceSchemaAndType(t, ctx, c.res)
			resp, inRaw := driveResourceRead(t, ctx, c.res, restrictOverrides(objType, ids))
			if !resp.Diagnostics.HasError() {
				t.Fatal("Read on a 200 with no body must error, got success")
			}
			if got := resp.Diagnostics.Errors()[0].Summary(); got != c.summary {
				t.Fatalf("summary = %q, want %q", got, c.summary)
			}
			if !resp.State.Raw.Equal(inRaw) {
				t.Fatalf("Read on an empty-body 200 must leave state untouched; state changed from %v to %v", inRaw, resp.State.Raw)
			}
		})
	}
}

// A non-JSON 200 at create must fail closed rather than nil-deref in the state mapper
// (firewall_rule's AsFirewallRule has a value receiver, so the nil pointer panics there).
func TestCreateFailsClosedOnEmptyBodyNonPg(t *testing.T) {
	ctx := t.Context()
	ids := map[string]tftypes.Value{
		"project_id":         strVal("pjx"),
		"location":           strVal("aws-us-east-1"),
		"name":               strVal("tf-acc-empty"),
		"firewall_reference": strVal("tf-acc-fw"),
		"cidr":               strVal("1.2.3.0/24"),
		"public_key":         strVal("ssh-ed25519 AAAA"),
	}
	cases := []struct {
		name    string
		res     resource.Resource
		summary string
	}{
		{"vm", &vmResource{}, "Empty response creating vm"},
		{"firewall", &firewallResource{}, "Empty response creating firewall"},
		{"firewall_rule", &firewallRuleResource{}, "Empty response creating firewall rule"},
		{"private_subnet", &privateSubnetResource{}, "Empty response creating private subnet"},
		{"project", &projectResource{}, "Empty response creating project"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setResourceClient(c.res, offlineClient(t, nonJSONOKServer(t)))
			_, objType := resourceSchemaAndType(t, ctx, c.res)
			resp := driveResourceCreate(t, ctx, c.res, restrictOverrides(objType, ids))
			if !resp.Diagnostics.HasError() {
				t.Fatal("Create on a 200 with no body must error, got success")
			}
			if got := resp.Diagnostics.Errors()[0].Summary(); got != c.summary {
				t.Fatalf("summary = %q, want %q", got, c.summary)
			}
		})
	}
}

func TestDatasourceReadFailsClosedOnEmptyBodyNonPg(t *testing.T) {
	ctx := t.Context()
	ids := map[string]tftypes.Value{
		"project_id":         strVal("pjx"),
		"location":           strVal("aws-us-east-1"),
		"name":               strVal("tf-acc-empty"),
		"id":                 strVal("idx"),
		"firewall_reference": strVal("tf-acc-fw"),
	}
	cases := []struct {
		name    string
		ds      datasource.DataSource
		summary string
	}{
		{"vm", &vmDataSource{}, "Empty response reading vm"},
		{"firewall", &firewallDataSource{}, "Empty response reading firewall"},
		{"firewall_rule", &firewallRuleDataSource{}, "Empty response reading firewall rule"},
		{"private_subnet", &privateSubnetDataSource{}, "Empty response reading private subnet"},
		{"project", &projectDataSource{}, "Empty response reading project"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setDatasourceClient(c.ds, offlineClient(t, nonJSONOKServer(t)))
			objType := datasourceObjType(t, ctx, c.ds)
			resp, inRaw := driveGenericDatasourceRead(t, ctx, c.ds, restrictOverrides(objType, ids))
			if !resp.Diagnostics.HasError() {
				t.Fatal("datasource Read on a 200 with no body must error, got success")
			}
			if got := resp.Diagnostics.Errors()[0].Summary(); got != c.summary {
				t.Fatalf("summary = %q, want %q", got, c.summary)
			}
			if !resp.State.Raw.Equal(inRaw) {
				t.Fatal("datasource Read on an empty-body 200 must not write partial state")
			}
		})
	}
}
