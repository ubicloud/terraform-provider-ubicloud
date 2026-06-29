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

// nonNullStateRaw builds a schema-valid object value with every attribute null except the
// identifying path fields, which get placeholders. A non-null object (vs. a null one) models
// a resource present in state, so a successful RemoveResource is observable as the state
// going null.
func nonNullStateRaw(objType tftypes.Object) tftypes.Value {
	ids := map[string]string{
		"project_id":    "pjx",
		"location":      "aws-us-east-1",
		"name":          "tf-acc-drift",
		"id":            "idx",
		"firewall_name": "tf-acc-fw",
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

// Every resource Read must drop the resource from state on a confirmed 404 instead of
// erroring. A resource deleted out of band (or a delete whose wait timed out, after which the
// backend later finishes the teardown) leaves a 404 on the next refresh; erroring there would
// wedge a phantom resource in state (gone server-side, stuck in Terraform) until a manual
// `state rm`. RemoveResource on 404 is the idiomatic drift handling and lets the next plan
// converge cleanly (recreate, or nothing to destroy). Postgres has its own coverage in
// postgres_wait_test.go; this exercises the other five resources, driven through the real
// resource.Resource interface against an httptest 404 (a network-boundary fake, not a client
// stub).
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
