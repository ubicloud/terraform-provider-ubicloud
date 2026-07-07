package provider

import (
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// A non-null object (vs a null one) models a resource present in state, so a successful
// RemoveResource is observable as the state going null.
func nonNullStateRaw(objType tftypes.Object) tftypes.Value {
	return mkRawFromSchema(objType, genericResourceIDs("tf-acc-drift"))
}

// Erroring on a refresh 404 would wedge an out-of-band-deleted resource in state until a
// manual `state rm`; RemoveResource lets the next plan converge. Postgres: postgres_wait_test.go.
func TestReadRemovesNonPostgresResourcesOnNotFound(t *testing.T) {
	ctx := t.Context()
	uc := offlineClient(t, statusServer(t, http.StatusNotFound, `{"error":{"code":404,"message":"not found"}}`))

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
