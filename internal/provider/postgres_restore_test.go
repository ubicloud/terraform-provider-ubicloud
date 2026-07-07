package provider

import (
	"testing"
	"time"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// restore_target is absent from the create body and every read response, so
// tfplugingen-openapi does not emit it; it is injected into the schema.
func TestPostgresRestoreTargetSchema(t *testing.T) {
	ctx := t.Context()
	schema := resource_postgres.PostgresResourceSchema(ctx)
	attr, ok := schema.Attributes["restore_target"]
	if !ok {
		t.Fatal("schema is missing the restore_target attribute")
	}
	if !attr.IsOptional() {
		t.Error("restore_target must be Optional")
	}
	if attr.IsComputed() {
		t.Error("restore_target must not be Computed (it is never read back)")
	}
	if attr.IsRequired() {
		t.Error("restore_target must not be Required (only a restore sets it)")
	}
}

// Pins the exact Description of the injected (plan_modifiers.jq ar()) AlsoRequires(parent)
// validator: a substring match on "parent" would also accept a wrong path like "parent_id".
func TestPostgresRestoreTargetAlsoRequiresParent(t *testing.T) {
	ctx := t.Context()
	s := resource_postgres.PostgresResourceSchema(ctx)
	attr, ok := s.Attributes["restore_target"]
	if !ok {
		t.Fatal("schema is missing the restore_target attribute")
	}
	want := stringvalidator.AlsoRequires(path.MatchRoot("parent")).Description(ctx)
	descs := postgresValidatorDescriptions(ctx, attr)
	found := false
	for _, d := range descs {
		if d == want {
			found = true
		}
	}
	if !found {
		t.Errorf("restore_target must carry AlsoRequires(path.MatchRoot(%q)) (a restore needs a source); want a validator with Description %q, got %v", "parent", want, descs)
	}
}

// Any known value, even blank, is a restore attempt: a malformed target must error on the
// restore path, never fall through to the read-replica branch (validity is the validator's job).
func TestPostgresHasRestoreTarget(t *testing.T) {
	cases := []struct {
		name string
		rt   types.String
		want bool
	}{
		{"null", types.StringNull(), false},
		{"unknown", types.StringUnknown(), false},
		{"empty is a (malformed) restore attempt", types.StringValue(""), true},
		{"whitespace is a (malformed) restore attempt", types.StringValue("   "), true},
		{"timestamp", types.StringValue("2026-06-24T11:00:00Z"), true},
	}
	for _, c := range cases {
		var m resource_postgres.PostgresModel
		m.RestoreTarget = c.rt
		if got := postgresHasRestoreTarget(&m); got != c.want {
			t.Errorf("%s: postgresHasRestoreTarget = %v, want %v", c.name, got, c.want)
		}
	}
}

// A restore inherits size from the source, so the primary-size guard must not fire for a
// restore config (parent + restore_target, no size).
func TestPostgresModifyPlanRestore(t *testing.T) {
	ctx := t.Context()
	r := &postgresResource{}
	schema := resource_postgres.PostgresResourceSchema(ctx)
	objType := postgresResourceSchemaObjType(t, ctx)

	modifyCreate := func(overrides map[string]tftypes.Value) diag.Diagnostics {
		req := resource.ModifyPlanRequest{
			State:  tfsdk.State{Schema: schema, Raw: tftypes.NewValue(objType, nil)}, // null state = create
			Config: tfsdk.Config{Schema: schema, Raw: postgresRaw(t, ctx, overrides)},
		}
		resp := &resource.ModifyPlanResponse{}
		r.ModifyPlan(ctx, req, resp)
		return resp.Diagnostics
	}

	target := tftypes.NewValue(tftypes.String, "2026-06-24T11:00:00Z")
	validRestore := map[string]tftypes.Value{
		"parent":         tftypes.NewValue(tftypes.String, "tf-acc-source"),
		"restore_target": target,
	}
	if d := modifyCreate(validRestore); d.HasError() {
		t.Errorf("valid restore (parent+restore_target, no size): unexpected error: %+v", d)
	}

	// With parent null ModifyPlan stays silent: the schema-level AlsoRequires(parent) is the
	// single error the user sees.
	noParent := map[string]tftypes.Value{"restore_target": target}
	if d := modifyCreate(noParent); d.HasError() {
		t.Errorf("restore_target without parent: ModifyPlan must defer to AlsoRequires, not emit a size error: %+v", d)
	}

	// An unknown restore_target (e.g. the source's computed latest_restore_time) must plan
	// clean; it resolves at apply, where a still-creating source's "" fails closed on time.Parse.
	computedTarget := map[string]tftypes.Value{
		"parent":         tftypes.NewValue(tftypes.String, "tf-acc-source"),
		"restore_target": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	}
	if d := modifyCreate(computedTarget); d.HasError() {
		t.Errorf("computed (unknown) restore_target with parent: must plan clean: %+v", d)
	}
}

// The restore create body is the read-replica narrow set plus restore_target, parsed from
// the user's RFC 3339 string into the time.Time the API expects.
func TestPostgresRestoreBody(t *testing.T) {
	ctx := t.Context()
	var m resource_postgres.PostgresModel
	m.Name = types.StringValue("tf-acc-restored")
	m.RestoreTarget = types.StringValue("2026-06-24T11:00:00Z")
	m.PgConfig = mkStringMap(t, map[string]string{"max_connections": "100"})
	m.PgbouncerConfig = types.MapNull(types.StringType)
	m.Tags = mkResourceTags(t, [][2]string{{"team", "data"}})

	body, diags := postgresRestoreBody(ctx, &m)
	if diags.HasError() {
		t.Fatalf("postgresRestoreBody diags: %+v", diags)
	}

	if body.Name != "tf-acc-restored" {
		t.Errorf("name = %q, want tf-acc-restored", body.Name)
	}
	want, _ := time.Parse(time.RFC3339, "2026-06-24T11:00:00Z")
	if !body.RestoreTarget.Equal(want) {
		t.Errorf("restore_target = %v, want %v", body.RestoreTarget, want)
	}
	if body.PgConfig == nil || (*body.PgConfig)["max_connections"] != "100" {
		t.Errorf("pg_config = %v, want {max_connections:100}", body.PgConfig)
	}
	if body.PgbouncerConfig != nil {
		t.Errorf("pgbouncer_config = %v, want nil when unset", *body.PgbouncerConfig)
	}
	if body.Tags == nil || len(*body.Tags) != 1 {
		t.Fatalf("tags = %v, want one tag", body.Tags)
	}
	if (*body.Tags)[0].Key != "team" || (*body.Tags)[0].Value != "data" {
		t.Errorf("tag = {%q:%q}, want {team:data}", (*body.Tags)[0].Key, (*body.Tags)[0].Value)
	}
}

func TestPostgresRestoreBodyOmitsUnknown(t *testing.T) {
	ctx := t.Context()
	var m resource_postgres.PostgresModel
	m.Name = types.StringValue("tf-acc-restored")
	m.RestoreTarget = types.StringValue("  2026-06-24T11:00:00Z  ")
	m.PgConfig = types.MapUnknown(types.StringType)
	m.PgbouncerConfig = types.MapUnknown(types.StringType)
	m.Tags = types.ListUnknown(resource_postgres.TagsValue{}.Type(ctx))

	body, diags := postgresRestoreBody(ctx, &m)
	if diags.HasError() {
		t.Fatalf("postgresRestoreBody diags: %+v", diags)
	}
	want, _ := time.Parse(time.RFC3339, "2026-06-24T11:00:00Z")
	if !body.RestoreTarget.Equal(want) {
		t.Errorf("restore_target = %v, want %v (padded input must be trimmed)", body.RestoreTarget, want)
	}
	if body.PgConfig != nil || body.PgbouncerConfig != nil || body.Tags != nil {
		t.Errorf("unknown config/tags must be omitted: pg=%v pgb=%v tags=%v", body.PgConfig, body.PgbouncerConfig, body.Tags)
	}
}

func TestPostgresRestoreBodyInvalidTarget(t *testing.T) {
	ctx := t.Context()
	var m resource_postgres.PostgresModel
	m.Name = types.StringValue("tf-acc-restored")
	m.RestoreTarget = types.StringValue("not-a-timestamp")

	_, diags := postgresRestoreBody(ctx, &m)
	if !diags.HasError() {
		t.Fatal("expected a diagnostic for an invalid restore_target, got none")
	}
	var found bool
	for _, d := range diags.Errors() {
		if d.Summary() != "Invalid restore_target" {
			continue
		}
		found = true
		wp, ok := d.(diag.DiagnosticWithPath)
		if !ok {
			t.Fatalf("Invalid restore_target diag must be attribute-scoped, got %T", d)
		}
		if !wp.Path().Equal(path.Root("restore_target")) {
			t.Errorf("attribute path = %s, want restore_target", wp.Path())
		}
	}
	if !found {
		t.Errorf("expected an \"Invalid restore_target\" attribute error, got %+v", diags)
	}
}

// restore_target is write-only (absent from every read response), so the read surface must
// leave the user-supplied value untouched.
func TestSetPostgresStateResourcePreservesRestoreTarget(t *testing.T) {
	ctx := t.Context()
	resp := sampleDetailedPostgresResponse()
	resp.ReadReplica = false
	resp.Parent = ptrTo("/location/aws-us-east-1/postgres/tf-acc-source")

	var m resource_postgres.PostgresModel
	m.RestoreTarget = types.StringValue("2026-06-24T11:00:00Z")

	if diags := setPostgresStateResource(ctx, &resp, &m); diags.HasError() {
		t.Fatalf("setPostgresStateResource diags: %+v", diags)
	}
	if got := m.RestoreTarget.ValueString(); got != "2026-06-24T11:00:00Z" {
		t.Errorf("restore_target = %q, want preserved user value (not touched by the read surface)", got)
	}
}
