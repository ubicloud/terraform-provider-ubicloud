package provider

import (
	"context"
	"testing"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func mkStringMap(t *testing.T, kv map[string]string) types.Map {
	t.Helper()
	m, diags := types.MapValueFrom(t.Context(), types.StringType, kv)
	if diags.HasError() {
		t.Fatalf("mkStringMap: %+v", diags)
	}
	return m
}

func mkResourceTags(t *testing.T, kv [][2]string) types.List {
	t.Helper()
	ctx := t.Context()
	tv := resource_postgres.TagsValue{}
	elems := make([]resource_postgres.TagsValue, 0, len(kv))
	for _, p := range kv {
		elems = append(elems, resource_postgres.NewTagsValueMust(tv.AttributeTypes(ctx), map[string]attr.Value{
			"key":   types.StringValue(p[0]),
			"value": types.StringValue(p[1]),
		}))
	}
	list, diags := types.ListValueFrom(ctx, tv.Type(ctx), elems)
	if diags.HasError() {
		t.Fatalf("mkResourceTags: %+v", diags)
	}
	return list
}

// The read-replica create body (POST .../read-replica) is the NARROW set
// {name, pg_config, pgbouncer_config, tags}; none of the parent-inherited create
// inputs (size/storage_size/version/ha_type/flavor) belong in it. Unset optional
// config/tags must be omitted (nil), not sent as empty.
func TestPostgresReadReplicaBody(t *testing.T) {
	ctx := t.Context()
	var m resource_postgres.PostgresModel
	m.Name = types.StringValue("tf-acc-child")
	m.PgConfig = mkStringMap(t, map[string]string{"max_connections": "100"})
	m.PgbouncerConfig = types.MapNull(types.StringType)
	m.Tags = mkResourceTags(t, [][2]string{{"team", "data"}})

	body, diags := postgresReadReplicaBody(ctx, &m)
	if diags.HasError() {
		t.Fatalf("postgresReadReplicaBody diags: %+v", diags)
	}

	if body.Name != "tf-acc-child" {
		t.Errorf("name = %q, want tf-acc-child", body.Name)
	}
	if body.PgConfig == nil || (*body.PgConfig)["max_connections"] != "100" {
		t.Errorf("pg_config = %v, want {max_connections:100}", body.PgConfig)
	}
	// Unset (null) pgbouncer_config is omitted, not an empty object.
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

// Unknown (computed-not-yet-resolved) config/tags must also be omitted: a replica
// created with only name+parent must not serialize {} or an unknown placeholder.
func TestPostgresReadReplicaBodyOmitsUnknown(t *testing.T) {
	ctx := t.Context()
	var m resource_postgres.PostgresModel
	m.Name = types.StringValue("tf-acc-child")
	m.PgConfig = types.MapUnknown(types.StringType)
	m.PgbouncerConfig = types.MapUnknown(types.StringType)
	m.Tags = types.ListUnknown(resource_postgres.TagsValue{}.Type(ctx))

	body, diags := postgresReadReplicaBody(ctx, &m)
	if diags.HasError() {
		t.Fatalf("postgresReadReplicaBody diags: %+v", diags)
	}
	if body.PgConfig != nil || body.PgbouncerConfig != nil || body.Tags != nil {
		t.Errorf("unknown config/tags must be omitted: pg=%v pgb=%v tags=%v", body.PgConfig, body.PgbouncerConfig, body.Tags)
	}
}

// parent is a create-time reference the user supplies (name or id); the response
// returns the parent's canonical PATH, a different string. setPostgresStateResource
// must preserve the user-supplied parent so the apply does not produce an
// inconsistent result and RequiresReplace does not churn on every plan.
func TestSetPostgresStateResourcePreservesUserParent(t *testing.T) {
	ctx := t.Context()
	resp := sampleDetailedPostgresResponse()
	resp.ReadReplica = true
	resp.Parent = ptrTo("/location/aws-us-east-1/postgres/tf-acc-rrparent")

	var m resource_postgres.PostgresModel
	m.Parent = types.StringValue("tf-acc-rrparent") // user-set reference (name form)

	if diags := setPostgresStateResource(ctx, &resp, &m); diags.HasError() {
		t.Fatalf("setPostgresStateResource diags: %+v", diags)
	}
	if got := m.Parent.ValueString(); got != "tf-acc-rrparent" {
		t.Errorf("parent = %q, want preserved user value %q (not the response path)", got, "tf-acc-rrparent")
	}
	if m.ReadReplica.IsNull() || !m.ReadReplica.ValueBool() {
		t.Errorf("read_replica = %v (null=%v), want known true", m.ReadReplica.ValueBool(), m.ReadReplica.IsNull())
	}
}

// postgresResourceSchemaObjType returns the resource schema's tftypes.Object type for
// building Plan/State values in tests without hand-listing every attribute.
func postgresResourceSchemaObjType(t *testing.T, ctx context.Context) tftypes.Object {
	t.Helper()
	objType, ok := resource_postgres.PostgresResourceSchema(ctx).Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatalf("schema terraform type is not tftypes.Object")
	}
	return objType
}

// postgresRaw builds a known object value for the resource schema with every attribute
// null except the supplied overrides.
func postgresRaw(t *testing.T, ctx context.Context, overrides map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	objType := postgresResourceSchemaObjType(t, ctx)
	vals := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for name, typ := range objType.AttributeTypes {
		if v, ok := overrides[name]; ok {
			vals[name] = v
		} else {
			vals[name] = tftypes.NewValue(typ, nil)
		}
	}
	return tftypes.NewValue(objType, vals)
}

// The create-time size/parent invariant: a primary (no parent) needs a known, non-empty
// size; a replica (parent set) inherits it; an unknown parent or size defers (resolved
// later, with createPostgresPrimary catching one that resolves empty).
func TestPostgresPrimaryMissingSize(t *testing.T) {
	size := types.StringValue("m8gd.large")
	cases := []struct {
		name   string
		parent types.String
		size   types.String
		want   bool
	}{
		{"primary missing size", types.StringNull(), types.StringNull(), true},
		{"primary empty size", types.StringNull(), types.StringValue(""), true},
		{"primary with size", types.StringNull(), size, false},
		{"replica inherits size", types.StringValue("tf-acc-rrparent"), types.StringNull(), false},
		{"padded replica inherits size", types.StringValue("  tf-acc-rrparent  "), types.StringNull(), false},
		{"blank (whitespace) parent is a primary missing size", types.StringValue("   "), types.StringNull(), true},
		{"unknown parent defers", types.StringUnknown(), types.StringNull(), false},
		{"unknown size defers", types.StringNull(), types.StringUnknown(), false},
	}
	for _, c := range cases {
		if got := postgresPrimaryMissingSize(c.parent, c.size); got != c.want {
			t.Errorf("%s: postgresPrimaryMissingSize = %v, want %v", c.name, got, c.want)
		}
	}
}

// postgresHasParent decides read-replica dispatch. A null, unknown, empty, or
// whitespace-only parent is NOT a replica reference; a real name (even padded) is. A
// blank parent must not route to the read-replica endpoint as a meaningless path.
func TestPostgresHasParent(t *testing.T) {
	cases := []struct {
		name   string
		parent types.String
		want   bool
	}{
		{"null", types.StringNull(), false},
		{"unknown", types.StringUnknown(), false},
		{"empty", types.StringValue(""), false},
		{"whitespace", types.StringValue("   "), false},
		{"name", types.StringValue("tf-acc-rrparent"), true},
		{"padded name", types.StringValue("  tf-acc-rrparent  "), true},
	}
	for _, c := range cases {
		var m resource_postgres.PostgresModel
		m.Parent = c.parent
		if got := postgresHasParent(&m); got != c.want {
			t.Errorf("%s: postgresHasParent = %v, want %v", c.name, got, c.want)
		}
	}
}

// ModifyPlan must reject a CREATE of a primary with no size (plan-time), accept a
// primary with size or a replica with parent, defer when size is unknown, and (the
// load-bearing gate) NOT fire on UPDATE, where an existing primary may drop size from
// config and keep the prior value via UseStateForUnknown.
func TestPostgresModifyPlanRequiresSizeOnCreate(t *testing.T) {
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

	if d := modifyCreate(nil); !d.HasError() {
		t.Error("create primary with neither size nor parent: expected a plan-time error, got none")
	}
	sizeOnly := map[string]tftypes.Value{"size": tftypes.NewValue(tftypes.String, "m8gd.large")}
	if d := modifyCreate(sizeOnly); d.HasError() {
		t.Errorf("create primary with size: unexpected error: %+v", d)
	}
	parentOnly := map[string]tftypes.Value{"parent": tftypes.NewValue(tftypes.String, "tf-acc-rrparent")}
	if d := modifyCreate(parentOnly); d.HasError() {
		t.Errorf("create replica with parent: unexpected error: %+v", d)
	}
	sizeUnknown := map[string]tftypes.Value{"size": tftypes.NewValue(tftypes.String, tftypes.UnknownValue)}
	if d := modifyCreate(sizeUnknown); d.HasError() {
		t.Errorf("create with unknown size: must defer, got error: %+v", d)
	}

	// UPDATE: prior state exists (non-null), config dropped size and has no parent. The
	// create-gate must skip so an omitted-but-state-carried size is not falsely rejected.
	updateReq := resource.ModifyPlanRequest{
		State:  tfsdk.State{Schema: schema, Raw: postgresRaw(t, ctx, nil)}, // non-null = existing
		Config: tfsdk.Config{Schema: schema, Raw: postgresRaw(t, ctx, nil)},
	}
	updateResp := &resource.ModifyPlanResponse{}
	r.ModifyPlan(ctx, updateReq, updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Errorf("update dropping size from config: must not be rejected (create-gated), got: %+v", updateResp.Diagnostics)
	}
}

// When the caller has not set parent (a normal pg, or an imported replica whose
// parent is still null in state), parent is filled from the response so the
// computed value resolves instead of staying unknown after apply.
func TestSetPostgresStateResourceFillsParentWhenUnset(t *testing.T) {
	ctx := t.Context()
	resp := sampleDetailedPostgresResponse()
	resp.ReadReplica = true
	resp.Parent = ptrTo("/location/aws-us-east-1/postgres/tf-acc-rrparent")

	var m resource_postgres.PostgresModel // Parent is the null zero value

	if diags := setPostgresStateResource(ctx, &resp, &m); diags.HasError() {
		t.Fatalf("setPostgresStateResource diags: %+v", diags)
	}
	if got := m.Parent.ValueString(); got != "/location/aws-us-east-1/postgres/tf-acc-rrparent" {
		t.Errorf("parent = %q, want filled from response path", got)
	}
}
