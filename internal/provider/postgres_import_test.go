package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"
)

// importState mirrors fwserver.ImportResourceState: a null-valued resp.State of the
// generated schema, so SetAttribute on the empty object is exercised for real.
func importState(t *testing.T, id string) *resource.ImportStateResponse {
	t.Helper()
	ctx := t.Context()
	r := &postgresResource{}
	schema := resource_postgres.PostgresResourceSchema(ctx)
	objType := postgresResourceSchemaObjType(t, ctx)
	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: schema, Raw: tftypes.NewValue(objType, nil)},
	}
	r.ImportState(ctx, resource.ImportStateRequest{ID: id}, resp)
	return resp
}

func TestPostgresImportStateValid(t *testing.T) {
	ctx := t.Context()
	resp := importState(t, "pjrp30gjk1d1e2jj34v9x0dq4rp,aws-us-east-1,tf-acc-pg-import")

	if resp.Diagnostics.HasError() {
		t.Fatalf("valid id: unexpected diagnostics: %+v", resp.Diagnostics)
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("valid id: resp.State.Raw is null, want the three attributes written")
	}

	for _, c := range []struct {
		attr string
		want string
	}{
		{"project_id", "pjrp30gjk1d1e2jj34v9x0dq4rp"},
		{"location", "aws-us-east-1"},
		{"name", "tf-acc-pg-import"},
	} {
		var got types.String
		if diags := resp.State.GetAttribute(ctx, path.Root(c.attr), &got); diags.HasError() {
			t.Fatalf("get %s: %+v", c.attr, diags)
		}
		if got.IsNull() || got.ValueString() != c.want {
			t.Errorf("%s = %q (null=%v), want %q", c.attr, got.ValueString(), got.IsNull(), c.want)
		}
	}
}

func TestPostgresImportStateMalformed(t *testing.T) {
	for _, id := range []string{
		"",                                // empty id (Split yields one empty token)
		"tf-acc-pg-import",                // one part
		"aws-us-east-1,tf-acc-pg-import",  // two parts
		"pj,aws-us-east-1,name,extra",     // four parts
		",aws-us-east-1,tf-acc-pg-import", // empty project_id
		"pj,,tf-acc-pg-import",            // empty location
		"pj,aws-us-east-1,",               // empty name
	} {
		resp := importState(t, id)

		if !resp.Diagnostics.HasError() {
			t.Errorf("id %q: expected an error diagnostic, got none", id)
			continue
		}
		found := false
		for _, d := range resp.Diagnostics.Errors() {
			if d.Summary() == "Unexpected Import Identifier" {
				found = true
			}
		}
		if !found {
			t.Errorf("id %q: expected \"Unexpected Import Identifier\" summary, got %+v", id, resp.Diagnostics)
		}
		if !resp.State.Raw.IsNull() {
			t.Errorf("id %q: resp.State.Raw must stay null on a malformed id, got %v", id, resp.State.Raw)
		}
	}
}
