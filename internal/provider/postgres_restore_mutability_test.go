package provider

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// postgresParentInheritedConfigured reports only known, set values (unknown defers to
// apply); flavor/private_subnet_name/restrict_by_default keep the static ConflictsWith(parent).
func TestPostgresParentInheritedConfigured(t *testing.T) {
	cases := []struct {
		name  string
		model resource_postgres.PostgresModel
		want  []string
	}{
		{"none set", resource_postgres.PostgresModel{}, nil},
		{"size only", resource_postgres.PostgresModel{Size: types.StringValue("m8gd.large")}, []string{"size"}},
		{"all four", resource_postgres.PostgresModel{
			Size:        types.StringValue("m8gd.large"),
			StorageSize: types.Int64Value(64),
			HaType:      types.StringValue("async"),
			Version:     types.StringValue("17"),
		}, []string{"size", "storage_size", "ha_type", "version"}},
		{"unknown defers", resource_postgres.PostgresModel{Size: types.StringUnknown()}, nil},
		{"explicit null ignored", resource_postgres.PostgresModel{Size: types.StringNull()}, nil},
	}
	for _, c := range cases {
		if got := postgresParentInheritedConfigured(&c.model); !slices.Equal(got, c.want) {
			t.Errorf("%s: postgresParentInheritedConfigured = %v, want %v", c.name, got, c.want)
		}
	}
}

// ConflictsWith sees only config (no prior state), so it cannot tell a locked read replica
// from a mutable restored primary; ModifyPlan, which has prior state, enforces the lock instead.
func TestPostgresMutableAttrsDropParentConflict(t *testing.T) {
	ctx := t.Context()
	s := resource_postgres.PostgresResourceSchema(ctx)
	for _, name := range []string{"size", "storage_size", "ha_type", "version"} {
		attr, ok := s.Attributes[name]
		if !ok {
			t.Fatalf("attribute %q not found in schema", name)
		}
		for _, d := range postgresValidatorDescriptions(ctx, attr) {
			if strings.Contains(d, "parent") {
				t.Errorf("%s must NOT carry a parent-referencing validator (a restored primary is mutable); got %q", name, d)
			}
		}
	}
}

func TestModifyPlanCreateRejectsInheritedInputsWithParent(t *testing.T) {
	ctx := t.Context()
	r := &postgresResource{}
	schema := resource_postgres.PostgresResourceSchema(ctx)
	objType := postgresResourceSchemaObjType(t, ctx)

	modifyCreate := func(overrides map[string]tftypes.Value) resource.ModifyPlanResponse {
		req := resource.ModifyPlanRequest{
			State:  tfsdk.State{Schema: schema, Raw: tftypes.NewValue(objType, nil)}, // null state = create
			Config: tfsdk.Config{Schema: schema, Raw: postgresRaw(t, ctx, overrides)},
		}
		resp := resource.ModifyPlanResponse{}
		r.ModifyPlan(ctx, req, &resp)
		return resp
	}

	parent := tftypes.NewValue(tftypes.String, "tf-acc-src")
	target := tftypes.NewValue(tftypes.String, "2026-06-24T11:00:00Z")

	reject := map[string]map[string]tftypes.Value{
		"replica with size":         {"parent": parent, "size": strRaw("m8gd.large")},
		"replica with storage_size": {"parent": parent, "storage_size": numRaw(64)},
		"replica with ha_type":      {"parent": parent, "ha_type": strRaw("async")},
		"replica with version":      {"parent": parent, "version": strRaw("17")},
		"restore with size":         {"parent": parent, "restore_target": target, "size": strRaw("m8gd.large")},
	}
	for name, over := range reject {
		if resp := modifyCreate(over); !resp.Diagnostics.HasError() {
			t.Errorf("%s: expected a plan-time error (inherited from parent), got none", name)
		}
	}

	clean := map[string]map[string]tftypes.Value{
		"bare replica": {"parent": parent},
		"bare restore": {"parent": parent, "restore_target": target},
	}
	for name, over := range clean {
		if resp := modifyCreate(over); resp.Diagnostics.HasError() {
			t.Errorf("%s: unexpected plan-time error: %+v", name, resp.Diagnostics)
		}
	}
}

// The backend rejects any PATCH or upgrade on a read replica (both routes gate on
// read_replica?), so the plan rejects first; the gate keys on the PRIOR state's read_replica.
func TestModifyPlanReadReplicaLocksInheritedInputs(t *testing.T) {
	ctx := t.Context()
	parentPath := strRaw("/location/aws-us-east-1/postgres/tf-acc-src")
	// base doubles as prior state and post-modifier plan skeleton, so only the attribute
	// under test reads as a change.
	base := func() map[string]tftypes.Value {
		return map[string]tftypes.Value{
			"read_replica": boolRaw(true),
			"parent":       parentPath,
			"size":         strRaw("m8gd.large"),
			"storage_size": numRaw(64),
			"ha_type":      strRaw("none"),
			"version":      strRaw("17"),
		}
	}

	cases := map[string]map[string]tftypes.Value{
		"set size":         {"size": strRaw("m16gd.large")},
		"set storage_size": {"storage_size": numRaw(128)},
		"set ha_type":      {"ha_type": strRaw("async")},
	}
	for name, change := range cases {
		plan := base()
		config := map[string]tftypes.Value{"parent": parentPath}
		for k, v := range change {
			plan[k] = v
			config[k] = v
		}
		resp := driveModifyPlanConfig(t, ctx, config, base(), plan)
		if !resp.Diagnostics.HasError() {
			t.Errorf("read replica %s: expected a plan-time error (replicas are inherited from the parent), got none", name)
		}
	}
}

func TestModifyPlanRestoredPrimaryAllowsInPlaceUpdate(t *testing.T) {
	ctx := t.Context()
	parentPath := strRaw("/location/aws-us-east-1/postgres/tf-acc-src")
	base := func() map[string]tftypes.Value {
		return map[string]tftypes.Value{
			"read_replica": boolRaw(false),
			"parent":       parentPath,
			"size":         strRaw("m8gd.large"),
			"storage_size": numRaw(64),
			"ha_type":      strRaw("none"),
			"version":      strRaw("16"),
		}
	}

	cases := map[string]map[string]tftypes.Value{
		"resize":         {"size": strRaw("m16gd.large")},
		"grow storage":   {"storage_size": numRaw(128)},
		"enable ha":      {"ha_type": strRaw("async")},
		"one-major bump": {"version": strRaw("17")},
	}
	for name, change := range cases {
		state := base()
		plan := base()
		config := map[string]tftypes.Value{"parent": parentPath}
		for k, v := range change {
			plan[k] = v
			config[k] = v
		}
		resp := driveModifyPlanConfig(t, ctx, config, state, plan)
		if resp.Diagnostics.HasError() {
			t.Errorf("restored primary %s: must plan cleanly (a restore is a mutable primary), got: %+v", name, resp.Diagnostics)
		}
	}
}

func TestUpdateRestoredPrimaryResizeDispatchesPatch(t *testing.T) {
	ctx := t.Context()
	parentPath := strRaw("/location/aws-us-east-1/postgres/tf-acc-src")
	stateOver := map[string]tftypes.Value{
		"read_replica": boolRaw(false),
		"parent":       parentPath,
		"size":         strRaw("m8gd.large"),
		"storage_size": numRaw(64),
		"ha_type":      strRaw("none"),
		"version":      strRaw("17"),
	}
	planOver := map[string]tftypes.Value{
		"read_replica": boolRaw(false),
		"parent":       parentPath,
		"size":         strRaw("m16gd.large"),
		"storage_size": numRaw(64),
		"ha_type":      strRaw("none"),
		"version":      strRaw("17"),
	}
	capRT, resp := runUpdate(t, ctx, stateOver, planOver)
	if resp.Diagnostics.HasError() {
		t.Fatalf("restored primary resize: unexpected diags: %+v", resp.Diagnostics)
	}
	var patch *capturedReq
	for i := range capRT.reqs {
		rq := capRT.reqs[i]
		if rq.Method == http.MethodPatch && strings.HasSuffix(rq.Path, "/postgres/tf-acc-pg") {
			patch = &rq
		}
	}
	if patch == nil {
		t.Fatalf("no resize PATCH .../postgres/tf-acc-pg issued; calls=%+v", capRT.dispatchReqs())
	}
	if body := bodyJSON(t, patch.Body); body["size"] != "m16gd.large" {
		t.Errorf("PATCH size = %v, want m16gd.large", body["size"])
	}
}

// tags rides the PATCH route the backend forbids on a replica, but is a valid replica
// CREATE input, so the lock keys on a CHANGE, not mere presence.
func TestModifyPlanReadReplicaLocksTagsChange(t *testing.T) {
	ctx := t.Context()
	parentPath := strRaw("/location/aws-us-east-1/postgres/tf-acc-src")
	base := func(tags tftypes.Value) map[string]tftypes.Value {
		return map[string]tftypes.Value{
			"read_replica": boolRaw(true),
			"parent":       parentPath,
			"size":         strRaw("m8gd.large"),
			"storage_size": numRaw(64),
			"ha_type":      strRaw("none"),
			"version":      strRaw("17"),
			"tags":         tags,
		}
	}
	oldTags := rawTags(t, ctx, [][2]string{{"team", "data"}})
	newTags := rawTags(t, ctx, [][2]string{{"team", "data"}, {"env", "prod"}})

	if resp := driveModifyPlanConfig(t, ctx,
		map[string]tftypes.Value{"parent": parentPath, "tags": newTags}, base(oldTags), base(newTags)); !resp.Diagnostics.HasError() {
		t.Error("read replica tags change: expected a plan-time error, got none")
	}

	if resp := driveModifyPlanConfig(t, ctx,
		map[string]tftypes.Value{"parent": parentPath, "tags": oldTags}, base(oldTags), base(oldTags)); resp.Diagnostics.HasError() {
		t.Errorf("read replica unchanged tags: must plan cleanly, got: %+v", resp.Diagnostics)
	}
}

// ModifyPlan defers unknown (interpolated) values, so the apply path carries its own
// backstop: a parent resolving to blank must fail before any POST.
func TestCreateRejectsBlankParentAtApply(t *testing.T) {
	ctx := t.Context()
	r, capRT := newCapturingPostgresResource(t)
	capRT.detailState = "running" // bound the test if the guard ever regresses
	resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
		"parent": strRaw("   "), // resolved-blank source, no restore_target -> would-be replica
		"size":   strRaw("m8gd.large"),
	})
	if !resp.Diagnostics.HasError() || resp.Diagnostics.Errors()[0].Summary() != "Blank parent" {
		t.Fatalf("want a \"Blank parent\" error, got %+v", resp.Diagnostics)
	}
	assertNoPost(t, capRT)
}

// driveUpdate bypasses ModifyPlan, exercising the apply-time backstop for a replica change
// that was unknown at plan: reject before any doomed dispatch.
func TestUpdateReadReplicaRejectsResizeAtApply(t *testing.T) {
	ctx := t.Context()
	parentPath := strRaw("/location/aws-us-east-1/postgres/tf-acc-src")
	fields := func(size tftypes.Value) map[string]tftypes.Value {
		return map[string]tftypes.Value{
			"read_replica": boolRaw(true),
			"parent":       parentPath,
			"size":         size,
			"storage_size": numRaw(64),
			"ha_type":      strRaw("none"),
			"version":      strRaw("17"),
		}
	}
	capRT, resp := runUpdate(t, ctx, fields(strRaw("m8gd.large")), fields(strRaw("m16gd.large")))
	if !resp.Diagnostics.HasError() || resp.Diagnostics.Errors()[0].Summary() != "Read replica cannot be modified" {
		t.Fatalf("want a \"Read replica cannot be modified\" error, got %+v", resp.Diagnostics)
	}
	for _, rq := range capRT.reqs {
		if rq.Method == http.MethodPatch {
			t.Errorf("a PATCH was issued for a replica resize: %s", rq.Path)
		}
	}
}

// The backend allows config, rename, and set-maintenance-window on a replica (no
// read_replica? gate on those routes); only the PATCH set and version are locked.
func TestModifyPlanReadReplicaAllowsNonPatchChange(t *testing.T) {
	ctx := t.Context()
	parentPath := strRaw("/location/aws-us-east-1/postgres/tf-acc-src")
	base := func(win tftypes.Value) map[string]tftypes.Value {
		return map[string]tftypes.Value{
			"read_replica":                boolRaw(true),
			"parent":                      parentPath,
			"size":                        strRaw("m8gd.large"),
			"storage_size":                numRaw(64),
			"ha_type":                     strRaw("none"),
			"version":                     strRaw("17"),
			"maintenance_window_start_at": win,
		}
	}
	config := map[string]tftypes.Value{"parent": parentPath, "maintenance_window_start_at": numRaw(14)}
	if resp := driveModifyPlanConfig(t, ctx, config, base(numRaw(2)), base(numRaw(14))); resp.Diagnostics.HasError() {
		t.Errorf("read replica maintenance-window change: must plan cleanly (allowed on a replica), got: %+v", resp.Diagnostics)
	}
}
