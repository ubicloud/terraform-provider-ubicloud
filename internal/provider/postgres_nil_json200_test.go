package provider

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"
)

// A nil body is not a 404: Read must error, not RemoveResource, and leave prior state untouched.
func TestReadFailsClosedOnEmptyBody(t *testing.T) {
	ctx := t.Context()
	r := newTestPostgresResource(t, nonJSONOKServer(t))
	resp := driveRead(t, ctx, r, nil)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected a fail-closed error on a 200 with no database body, got success")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); got != "Empty response reading postgres database" {
		t.Fatalf("summary = %q, want Empty response reading postgres database", got)
	}
	if !resp.State.Raw.Equal(mkPGRaw(t, ctx, nil)) {
		t.Fatal("Read on an empty-body 200 must leave state untouched (no RemoveResource, no overwrite)")
	}
}

func TestDatasourceReadFailsClosedOnEmptyBody(t *testing.T) {
	ctx := t.Context()
	d := &postgresDataSource{uc: offlineClient(t, nonJSONOKServer(t))}
	over := map[string]tftypes.Value{
		"project_id": strRaw("pjtest"),
		"location":   strRaw("aws-us-east-1"),
		"name":       strRaw("tf-acc-pg"),
	}
	resp, inRaw := driveGenericDatasourceRead(t, ctx, d, over)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected a fail-closed error on a 200 with no database body, got success")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); got != "Empty response reading postgres database" {
		t.Fatalf("summary = %q, want Empty response reading postgres database", got)
	}
	if !resp.State.Raw.Equal(inRaw) {
		t.Fatal("datasource Read on an empty-body 200 must not write partial state")
	}
}

// If the post-rename re-read nil-derefs, persistRenamedNameOnError never runs, state keeps the
// OLD name, and the next refresh 404s into a recreate that name-conflicts with the renamed row.
func TestUpdateRenamePersistsNameWhenReadReturnsEmptyBody(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{detailGetNonJSON: true}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r, nil, map[string]tftypes.Value{"name": strRaw("tf-acc-pg-renamed")})

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected a fail-closed error when the post-rename re-read returns an empty body")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); got != "Empty response reading postgres database after update" {
		t.Fatalf("summary = %q, want Empty response reading postgres database after update", got)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if out.Name.ValueString() != "tf-acc-pg-renamed" {
		t.Errorf("state name = %q, want the new name persisted despite the empty-body re-read", out.Name.ValueString())
	}

	// Non-vacuous: the rename POST must have landed before the failed re-read.
	var sawRename, sawGetAfterRename bool
	for _, rq := range capRT.reqs {
		if rq.Method == http.MethodPost && strings.HasSuffix(rq.Path, "/rename") {
			sawRename = true
		}
		if sawRename && rq.Method == http.MethodGet {
			sawGetAfterRename = true
		}
	}
	if !sawRename || !sawGetAfterRename {
		t.Fatalf("expected a rename POST followed by a detail GET, got %+v", capRT.reqs)
	}
}
