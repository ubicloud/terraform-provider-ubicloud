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

// The read-replica create body is the narrow {name, pg_config, pgbouncer_config, tags};
// the parent-inherited inputs (size/storage_size/version/ha_type/flavor) do not belong in it.
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

// The response reports parent as its canonical PATH, not the user-supplied name/id;
// adopting it would trip inconsistent-result and RequiresReplace churn on every plan.
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

func postgresResourceSchemaObjType(t *testing.T, ctx context.Context) tftypes.Object {
	t.Helper()
	objType, ok := resource_postgres.PostgresResourceSchema(ctx).Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatalf("schema terraform type is not tftypes.Object")
	}
	return objType
}

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

// pgNullStateRaw is the create-time State.Raw the framework seeds (server_createresource.go): a null
// object of the schema type, not the plan. Seeding it keeps a no-persist path's state null, so a
// negative assertion checks resp.State.Raw.IsNull() rather than reading plan values back.
func pgNullStateRaw(t *testing.T, ctx context.Context) tftypes.Value {
	t.Helper()
	return tftypes.NewValue(postgresResourceSchemaObjType(t, ctx), nil)
}

// An unknown parent/size defers to apply time; createPostgresPrimary catches one that
// resolves empty.
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
		{"primary whitespace size", types.StringNull(), types.StringValue("   "), true},
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

// postgresHasParent decides read-replica dispatch; a blank parent must not route to the
// read-replica endpoint as a meaningless path.
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

// The create-only gate must NOT fire on UPDATE, where an existing primary may drop size
// from config and keep the prior value via UseStateForUnknown.
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

// An unset parent (normal pg, or an imported replica) must fill from the response so the
// computed resolves instead of staying unknown after apply.
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
