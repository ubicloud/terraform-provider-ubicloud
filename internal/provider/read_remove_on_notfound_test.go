package provider

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"
)

// notFoundServer answers every request with 404, modeling a resource deleted out of band
// (or a delete that finished after its wait timed out).
func notFoundServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A non-null object (vs a null one) models a resource present in state, so a successful
// RemoveResource is observable as the state going null.
func nonNullStateRaw(objType tftypes.Object) tftypes.Value {
	ids := map[string]string{
		"project_id":         "pjx",
		"location":           "aws-us-east-1",
		"name":               "tf-acc-drift",
		"id":                 "idx",
		"firewall_reference": "tf-acc-fw",
	}
	vals := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for name, typ := range objType.AttributeTypes {
		if s, ok := ids[name]; ok && typ.Is(tftypes.String) {
			vals[name] = tftypes.NewValue(tftypes.String, s)
		} else {
			vals[name] = tftypes.NewValue(typ, nil)
		}
	}
	return tftypes.NewValue(objType, vals)
}

// Erroring on a refresh 404 would wedge an out-of-band-deleted resource in state until a
// manual `state rm`; RemoveResource lets the next plan converge. Postgres: postgres_wait_test.go.
func TestReadRemovesNonPostgresResourcesOnNotFound(t *testing.T) {
	ctx := t.Context()
	srv := notFoundServer(t)
	client, err := ubicloud_client.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithResponses: %v", err)
	}
	uc := &UbicloudClient{client: client}

	cases := []struct {
		name string
		res  resource.Resource
	}{
		{"vm", &vmResource{uc: uc}},
		{"firewall", &firewallResource{uc: uc}},
		{"firewall_rule", &firewallRuleResource{uc: uc}},
		{"private_subnet", &privateSubnetResource{uc: uc}},
		{"project", &projectResource{uc: uc}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var sresp resource.SchemaResponse
			c.res.Schema(ctx, resource.SchemaRequest{}, &sresp)
			objType, ok := sresp.Schema.Type().TerraformType(ctx).(tftypes.Object)
			if !ok {
				t.Fatalf("%s: schema terraform type is not tftypes.Object", c.name)
			}

			raw := nonNullStateRaw(objType)
			req := resource.ReadRequest{State: tfsdk.State{Schema: sresp.Schema, Raw: raw}}
			resp := &resource.ReadResponse{State: tfsdk.State{Schema: sresp.Schema, Raw: raw}}
			c.res.Read(ctx, req, resp)

			if resp.Diagnostics.HasError() {
				t.Fatalf("%s: Read on a 404 must not error (idiomatic drift handling), got: %+v", c.name, resp.Diagnostics)
			}
			if !resp.State.Raw.IsNull() {
				t.Fatalf("%s: Read on a 404 must RemoveResource (null state), but the resource is still present in state", c.name)
			}
		})
	}
}
