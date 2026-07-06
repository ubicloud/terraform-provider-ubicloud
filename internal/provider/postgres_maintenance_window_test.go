package provider

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func setMaintenanceWindowReqs(capRT *captureRT) []capturedReq {
	var out []capturedReq
	for _, r := range capRT.reqs {
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/set-maintenance-window") {
			out = append(out, r)
		}
	}
	return out
}

func TestPostgresMaintenanceWindowIsComputedOptional(t *testing.T) {
	ctx := t.Context()
	s := resource_postgres.PostgresResourceSchema(ctx)
	attr, ok := s.Attributes["maintenance_window_start_at"]
	if !ok {
		t.Fatal("schema missing maintenance_window_start_at")
	}
	if !attr.IsOptional() {
		t.Error("maintenance_window_start_at must be Optional (a settable input)")
	}
	if !attr.IsComputed() {
		t.Error("maintenance_window_start_at must stay Computed (server reports it when unset)")
	}
}

// The window is not a create-body field; it is applied through the separate POST
// .../set-maintenance-window once the database exists.
func TestCreateDispatchesSetMaintenanceWindowWhenConfigured(t *testing.T) {
	ctx := context.Background()
	r, capRT := newCapturingPostgresResource(t)
	capRT.detailState = "running"
	resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
		"size":                        strRaw("m8gd.large"),
		"storage_size":                numRaw(64),
		"maintenance_window_start_at": numRaw(3),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	mw := setMaintenanceWindowReqs(capRT)
	if len(mw) != 1 {
		t.Fatalf("set-maintenance-window calls = %d (%+v), want 1", len(mw), mw)
	}
	wantPath := "/postgres/" + out.Name.ValueString() + "/set-maintenance-window"
	if !strings.HasSuffix(mw[0].Path, wantPath) {
		t.Errorf("path = %s, want suffix %s", mw[0].Path, wantPath)
	}
	b := bodyJSON(t, mw[0].Body)
	if got, ok := b["maintenance_window_start_at"]; !ok || got != float64(3) {
		t.Errorf("body maintenance_window_start_at = %v (present=%v), want 3: %s", got, ok, mw[0].Body)
	}
	if out.MaintenanceWindowStartAt.IsNull() || out.MaintenanceWindowStartAt.ValueInt64() != 3 {
		t.Errorf("state maintenance_window_start_at null=%v val=%d, want 3",
			out.MaintenanceWindowStartAt.IsNull(), out.MaintenanceWindowStartAt.ValueInt64())
	}
}

func TestCreateNoMaintenanceWindowNoDispatch(t *testing.T) {
	ctx := context.Background()
	r, capRT := newCapturingPostgresResource(t)
	capRT.detailState = "running"
	resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
		"size":         strRaw("m8gd.large"),
		"storage_size": numRaw(64),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	if mw := setMaintenanceWindowReqs(capRT); len(mw) != 0 {
		t.Errorf("unset window must not dispatch set-maintenance-window, got %+v", mw)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if !out.MaintenanceWindowStartAt.IsNull() {
		t.Errorf("state maintenance_window_start_at = %d, want null (server-chosen)", out.MaintenanceWindowStartAt.ValueInt64())
	}
}

// A failed post-create window POST must not discard the created database: partial state
// keeps it tracked for replacement instead of orphaning it into a name conflict.
func TestCreateSetMaintenanceWindowErrorSurfacesAndPersists(t *testing.T) {
	ctx := context.Background()
	capRT := &captureRT{detailState: "running", setMWStatus: http.StatusInternalServerError}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
		"size":                        strRaw("m8gd.large"),
		"storage_size":                numRaw(64),
		"maintenance_window_start_at": numRaw(3),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed set-maintenance-window at create must surface an error")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); got != "Unexpected HTTP status code setting postgres maintenance window" {
		t.Fatalf("summary = %q, want the set-maintenance-window status error", got)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if out.Name.ValueString() != "tf-acc-pg" {
		t.Errorf("state name = %q, want the created name persisted despite the failed window POST", out.Name.ValueString())
	}
}

// The trailing re-read reports the sample's null window, so a state of 5 proves the plan
// value is held, not the read-back.
func TestUpdateDispatchMaintenanceWindowOnly(t *testing.T) {
	ctx := t.Context()
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{"maintenance_window_start_at": numRaw(3)},
		map[string]tftypes.Value{"maintenance_window_start_at": numRaw(5)},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	d := capRT.dispatchReqs()
	if len(d) != 1 || d[0].Method != http.MethodPost || !strings.HasSuffix(d[0].Path, "/postgres/tf-acc-pg/set-maintenance-window") {
		t.Fatalf("dispatch = %+v, want one POST .../postgres/tf-acc-pg/set-maintenance-window", d)
	}
	b := bodyJSON(t, d[0].Body)
	if got := b["maintenance_window_start_at"]; got != float64(5) {
		t.Errorf("body maintenance_window_start_at = %v, want 5: %s", got, d[0].Body)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if out.MaintenanceWindowStartAt.IsNull() || out.MaintenanceWindowStartAt.ValueInt64() != 5 {
		t.Errorf("state maintenance_window_start_at null=%v val=%d, want held 5",
			out.MaintenanceWindowStartAt.IsNull(), out.MaintenanceWindowStartAt.ValueInt64())
	}
}

func TestUpdateMaintenanceWindowFirstSetFromNull(t *testing.T) {
	ctx := t.Context()
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{}, // state omits the window (null)
		map[string]tftypes.Value{"maintenance_window_start_at": numRaw(5)},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	d := capRT.dispatchReqs()
	if len(d) != 1 || !strings.HasSuffix(d[0].Path, "/set-maintenance-window") {
		t.Fatalf("dispatch = %+v, want one POST .../set-maintenance-window for a first set", d)
	}
	b := bodyJSON(t, d[0].Body)
	if got, ok := b["maintenance_window_start_at"]; !ok || got != float64(5) {
		t.Errorf("body maintenance_window_start_at = %v (present=%v), want 5: %s", got, ok, d[0].Body)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if out.MaintenanceWindowStartAt.IsNull() || out.MaintenanceWindowStartAt.ValueInt64() != 5 {
		t.Errorf("state maintenance_window_start_at null=%v val=%d, want held 5",
			out.MaintenanceWindowStartAt.IsNull(), out.MaintenanceWindowStartAt.ValueInt64())
	}
}

// Hour 0 (midnight) is a real value distinct from unset; it must not read as "no change".
func TestUpdateDispatchMaintenanceWindowZero(t *testing.T) {
	ctx := t.Context()
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{"maintenance_window_start_at": numRaw(5)},
		map[string]tftypes.Value{"maintenance_window_start_at": numRaw(0)},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	mw := setMaintenanceWindowReqs(capRT)
	if len(mw) != 1 {
		t.Fatalf("set-maintenance-window calls = %d (%+v), want 1 for a change to midnight", len(mw), mw)
	}
	b := bodyJSON(t, mw[0].Body)
	if got, ok := b["maintenance_window_start_at"]; !ok || got != float64(0) {
		t.Errorf("body maintenance_window_start_at = %v (present=%v), want explicit 0: %s", got, ok, mw[0].Body)
	}
}

func TestUpdateMaintenanceWindowUnchangedNoDispatch(t *testing.T) {
	ctx := t.Context()
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{"size": strRaw("m8gd.large"), "maintenance_window_start_at": numRaw(3)},
		map[string]tftypes.Value{"size": strRaw("m8gd.xlarge"), "maintenance_window_start_at": numRaw(3)},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	if mw := setMaintenanceWindowReqs(capRT); len(mw) != 0 {
		t.Errorf("unchanged window must not dispatch set-maintenance-window, got %+v", mw)
	}
	if d := capRT.dispatchReqs(); len(d) != 1 || d[0].Method != http.MethodPatch {
		t.Errorf("want exactly the size PATCH, got %+v", d)
	}
}

// An unmanaged window is pinned to prior by UseStateForUnknown; adopting the re-read's
// drifted value would diverge from the pinned plan and raise inconsistent-result-after-apply.
func TestUpdateMaintenanceWindowUnmanagedHoldsPlanAgainstDrift(t *testing.T) {
	ctx := t.Context()
	drift := sampleDetailedPostgresResponse()
	drift.MaintenanceWindowStartAt = ptrTo(9) // server drifted the window to 9 out of band
	capRT := &captureRT{detailBody: &drift}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{"size": strRaw("m8gd.large"), "maintenance_window_start_at": numRaw(3)},
		map[string]tftypes.Value{"size": strRaw("m8gd.xlarge"), "maintenance_window_start_at": numRaw(3)},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	if mw := setMaintenanceWindowReqs(capRT); len(mw) != 0 {
		t.Errorf("unchanged window must not dispatch set-maintenance-window, got %+v", mw)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if out.MaintenanceWindowStartAt.ValueInt64() != 3 {
		t.Errorf("state maintenance_window_start_at = %d, want held plan 3 (not drifted 9)", out.MaintenanceWindowStartAt.ValueInt64())
	}
}

func TestUpdateMaintenanceWindowWithVersionRejected(t *testing.T) {
	ctx := t.Context()
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{"version": strRaw("16"), "maintenance_window_start_at": numRaw(3)},
		map[string]tftypes.Value{"version": strRaw("17"), "maintenance_window_start_at": numRaw(5)},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("version upgrade combined with a maintenance-window change must be rejected")
	}
	if len(capRT.reqs) != 0 {
		t.Errorf("rejected combined change must issue no REST calls, got %+v", capRT.reqs)
	}
}

func TestUpdateMaintenanceWindowBeforeRename(t *testing.T) {
	ctx := t.Context()
	capRT, resp := runUpdate(t, ctx,
		map[string]tftypes.Value{"maintenance_window_start_at": numRaw(3)},
		map[string]tftypes.Value{"name": strRaw("tf-acc-pg-renamed"), "maintenance_window_start_at": numRaw(5)},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	d := capRT.dispatchReqs()
	if len(d) != 2 {
		t.Fatalf("dispatch calls = %d (%+v), want set-maintenance-window then rename", len(d), d)
	}
	if d[0].Method != http.MethodPost || !strings.HasSuffix(d[0].Path, "/postgres/tf-acc-pg/set-maintenance-window") {
		t.Errorf("call[0] = %s %s, want set-maintenance-window on old name", d[0].Method, d[0].Path)
	}
	if d[1].Method != http.MethodPost || !strings.HasSuffix(d[1].Path, "/postgres/tf-acc-pg/rename") {
		t.Errorf("call[1] = %s %s, want rename last", d[1].Method, d[1].Path)
	}
}

func TestUpdateSetMaintenanceWindowNon200SurfacesError(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{setMWStatus: http.StatusInternalServerError}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{"maintenance_window_start_at": numRaw(3)},
		map[string]tftypes.Value{"maintenance_window_start_at": numRaw(5)},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a non-200 set-maintenance-window must surface an error")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); got != "Unexpected HTTP status code setting postgres maintenance window" {
		t.Fatalf("summary = %q, want the set-maintenance-window status error", got)
	}
}

func TestUpdateSetMaintenanceWindowTransportErrorSurfacesError(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{setMWTransportErr: true}
	r := newPostgresResourceWithRT(t, capRT)
	resp := driveUpdate(t, ctx, r,
		map[string]tftypes.Value{"maintenance_window_start_at": numRaw(3)},
		map[string]tftypes.Value{"maintenance_window_start_at": numRaw(5)},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a transport error on set-maintenance-window must surface an error")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(got, "Error setting postgres maintenance window") {
		t.Fatalf("summary = %q, want the set-maintenance-window transport error", got)
	}
}
