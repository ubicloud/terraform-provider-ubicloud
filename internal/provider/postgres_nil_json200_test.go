package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// The 200 text/html page a proxy or LB answers with in front of the API: the generated client
// fills JSON200 only when Content-Type contains "json", so this parses as err==nil, JSON200 nil.
func interposedPageServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// driveDatasourceRead also returns the input config raw so callers can assert no partial state was written.
func driveDatasourceRead(t *testing.T, ctx context.Context, d *postgresDataSource, over map[string]tftypes.Value) (*datasource.ReadResponse, tftypes.Value) {
	t.Helper()
	var sresp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &sresp)
	objType, ok := sresp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatalf("datasource schema terraform type is not tftypes.Object")
	}
	raw := mkRawFromSchema(objType, over)
	req := datasource.ReadRequest{Config: tfsdk.Config{Schema: sresp.Schema, Raw: raw}}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: sresp.Schema, Raw: raw}}
	d.Read(ctx, req, resp)
	return resp, raw
}

// A nil body is not a 404: Read must error, not RemoveResource, and leave prior state untouched.
func TestReadFailsClosedOnEmptyBody(t *testing.T) {
	ctx := t.Context()
	r := newTestPostgresResource(t, interposedPageServer(t))
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
	d := &postgresDataSource{uc: offlineClient(t, interposedPageServer(t))}
	over := map[string]tftypes.Value{
		"project_id": strRaw("pjtest"),
		"location":   strRaw("aws-us-east-1"),
		"name":       strRaw("tf-acc-pg"),
	}
	resp, inRaw := driveDatasourceRead(t, ctx, d, over)
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
