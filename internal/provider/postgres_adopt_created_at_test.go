package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// runTimeoutAdoptCreate stalls the dispatch POST so the create deadline fires and the adopt
// path runs; the adopt GET answers a running row stamped created_at = adoptCreatedAt().
func runTimeoutAdoptCreate(t *testing.T, adoptCreatedAt func() time.Time) (*resource.CreateResponse, resource_postgres.PostgresModel) {
	t.Helper()
	ctx := context.Background()
	withFastPoll(t)
	withShortCreateTimeout(t, 100*time.Millisecond)
	withFastAdoptBudget(t, 2*time.Second)

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			<-release // the dispatch POST never responds, so the create deadline fires
			return
		}
		body := pgBodyForState(postgresStateRunning)
		body.Name = "tf-acc-pg"
		body.CreatedAt = adoptCreatedAt()
		writePGJSON(w, http.StatusOK, body)
	}))
	defer srv.Close()
	defer close(release)

	r := newTestPostgresResource(t, srv)
	schema := resource_postgres.PostgresResourceSchema(ctx)
	planRaw := mkPGRaw(t, ctx, map[string]tftypes.Value{
		"size":         strRaw("m8gd.large"),
		"storage_size": numRaw(64),
	})
	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: planRaw}}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schema, Raw: planRaw}}

	done := make(chan struct{})
	go func() {
		r.Create(ctx, req, resp)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Create ignored its create timeout on a stuck POST (it hung)")
	}

	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	return resp, out
}

// A row born at or after dispatch is this create's (committed but timed out): adopt it so the
// next apply replaces it instead of orphaning into a name conflict; the timeout diag still taints.
func TestCreateTimeoutAdoptsOwnFreshRow(t *testing.T) {
	sampleID := sampleDetailedPostgresResponse().Id
	resp, out := runTimeoutAdoptCreate(t, func() time.Time { return time.Now() })

	if !resp.Diagnostics.HasError() {
		t.Fatal("a timed-out create must still surface the timeout error")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(got, "Timeout while creating") {
		t.Fatalf("summary = %q, want a create-timeout classification", got)
	}
	if out.Id.ValueString() != sampleID {
		t.Errorf("adopted state id = %q, want the created row %q", out.Id.ValueString(), sampleID)
	}
}

// A lost or delayed name-conflict 500 can surface as this timeout, so a row predating the
// dispatch may be another owner's database: adopting it would let a later apply destroy it.
func TestCreateTimeoutDoesNotAdoptForeignOlderRow(t *testing.T) {
	resp, out := runTimeoutAdoptCreate(t, func() time.Time { return time.Now().Add(-time.Hour) })

	if !resp.Diagnostics.HasError() {
		t.Fatal("a timed-out create must still surface the timeout error")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(got, "Timeout while creating") {
		t.Fatalf("summary = %q, want a create-timeout classification", got)
	}
	if !out.Id.IsNull() {
		t.Errorf("a pre-existing foreign row must not be adopted, but state persisted id %q", out.Id.ValueString())
	}
}
