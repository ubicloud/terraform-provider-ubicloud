package provider

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/planmodifiers"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/validators"
)

// The phantom this file guards: terraform core proposes null for config-null tags and the no-op gate
// compares it to prior BEFORE plan modifiers run, so only prior tags=null closes it (accepted tradeoff: invisible server-side tag drift).

// A round-tripped tags element deserializes via the schema's custom type as
// resource_postgres.TagsValue (the state mappers emit the shared datasource TagsValue).
func firstTag(t *testing.T, v resource_postgres.PostgresModel) (string, string) {
	t.Helper()
	elems := v.Tags.Elements()
	if len(elems) != 1 {
		t.Fatalf("tags: want exactly one element, got %d (%v)", len(elems), elems)
	}
	tag, ok := elems[0].(resource_postgres.TagsValue)
	if !ok {
		t.Fatalf("tags element type %T, want resource_postgres.TagsValue", elems[0])
	}
	return tag.Key.ValueString(), tag.Value.ValueString()
}

func TestReadPreservesConfigNullTags(t *testing.T) {
	ctx := t.Context()
	r := newPostgresResourceWithRT(t, &captureRT{}) // sample response carries tags=[{team,data}]

	resp := driveRead(t, ctx, r, map[string]tftypes.Value{
		"id": strRaw("pgn30gjk1d1e2jj34v9x0dq4rp"), // id set => a managed refresh, not an import
		// tags omitted => prior state tags is null (a config that never set tags)
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if !out.Tags.IsNull() {
		t.Errorf("config-null tags must stay null on refresh (no phantom), got %v", out.Tags)
	}
}

func TestReadHydratesTagsOnImport(t *testing.T) {
	ctx := t.Context()
	r := newPostgresResourceWithRT(t, &captureRT{})

	resp := driveRead(t, ctx, r, map[string]tftypes.Value{
		// no id => the post-ImportState Read; everything hydrates from the server
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if out.Tags.IsNull() {
		t.Fatal("import must hydrate tags from the server, got null")
	}
	if k, v := firstTag(t, out); k != "team" || v != "data" {
		t.Errorf("imported tag = {%q:%q}, want {team:data}", k, v)
	}
}

func TestReadRefreshesExplicitTags(t *testing.T) {
	ctx := t.Context()
	r := newPostgresResourceWithRT(t, &captureRT{})

	resp := driveRead(t, ctx, r, map[string]tftypes.Value{
		"id":   strRaw("pgn30gjk1d1e2jj34v9x0dq4rp"),
		"tags": rawTags(t, ctx, [][2]string{{"stale", "old"}}), // prior managed tags, server drifted
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if out.Tags.IsNull() {
		t.Fatal("explicit tags must refresh (drift visible), got null")
	}
	if k, v := firstTag(t, out); k != "team" || v != "data" {
		t.Errorf("refreshed tag = {%q:%q}, want the server's {team:data}", k, v)
	}
}

// KNOWN LIMITATION, pinned deliberately: Read has no config and keys on "prior tags is null",
// so a config-null imported resource (prior non-null) re-hydrates and keeps phantoming.
func TestReadPostImportRefreshHydratesServerTags(t *testing.T) {
	ctx := t.Context()
	r := newPostgresResourceWithRT(t, &captureRT{})

	resp := driveRead(t, ctx, r, map[string]tftypes.Value{
		"id":   strRaw("pgn30gjk1d1e2jj34v9x0dq4rp"),           // id set => a post-import refresh, not the import Read
		"tags": rawTags(t, ctx, [][2]string{{"team", "data"}}), // import hydrated these into prior state
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if out.Tags.IsNull() {
		t.Fatal("post-import refresh currently hydrates server tags (known limitation); got null")
	}
	if k, v := firstTag(t, out); k != "team" || v != "data" {
		t.Errorf("post-import refresh tag = {%q:%q}, want the server's {team:data}", k, v)
	}
}

func TestCreateRoundTripsExplicitTags(t *testing.T) {
	ctx := t.Context()
	r, capRT := newCapturingPostgresResource(t)
	capRT.detailState = "running"

	resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
		"size":         strRaw("m8gd.large"),
		"storage_size": numRaw(64),
		"tags":         rawTags(t, ctx, [][2]string{{"team", "data"}}),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if out.Tags.IsNull() {
		t.Fatal("explicit tags must round-trip into state, got null")
	}
	if k, v := firstTag(t, out); k != "team" || v != "data" {
		t.Errorf("created tag = {%q:%q}, want {team:data}", k, v)
	}
}

// tags is the only list_nested attribute with a plan modifier, and a wrong jq path fails
// silently (jq creates it without error), so lock the LIST row specifically.
func TestPostgresTagsListPlanModifierLive(t *testing.T) {
	ctx := t.Context()
	s := resource_postgres.PostgresResourceSchema(ctx)
	attr, ok := s.Attributes["tags"]
	if !ok {
		t.Fatal("attribute \"tags\" not found in schema")
	}
	la, ok := attr.(schema.ListNestedAttribute)
	if !ok {
		t.Fatalf("tags: expected schema.ListNestedAttribute, got %T", attr)
	}
	if len(la.PlanModifiers) == 0 {
		t.Fatal("tags carries NO plan modifier: the plan_modifiers.jq tags row is dead")
	}
	descs := postgresPlanModifierDescriptions(ctx, attr)
	if !descsContain(descs, descUseStateForUnknown) {
		t.Errorf("tags must carry UseStateForUnknown, got %v", descs)
	}
}

// A plan_modifiers.jq row against a renamed attribute or a wrong spec key no-ops silently, so each row
// must match a marker UNIQUE to its block's injection (version's codegen-native OneOf would mask a dead row).
func TestPlanModifiersJqChainHygiene(t *testing.T) {
	ctx := t.Context()
	src, err := os.ReadFile("../../config/plan_modifiers.jq")
	if err != nil {
		t.Fatalf("read plan_modifiers.jq: %v", err)
	}
	blocks := strings.Split(string(src), "|= map(")
	if len(blocks) != 3 {
		t.Fatalf("expected exactly two `|= map(` blocks in plan_modifiers.jq, got %d; the file structure changed", len(blocks)-1)
	}

	// The only non-attribute .name branch is the resource selector select(.name == "postgres").
	re := regexp.MustCompile(`\.name == "([^"]+)"`)
	s := resource_postgres.PostgresResourceSchema(ctx)

	assertRows := func(block, kind string, wiring func(schema.Attribute) []string, injected func(string) bool) {
		matched := 0
		seen := map[string]bool{}
		for _, m := range re.FindAllStringSubmatch(block, -1) {
			name := m[1]
			if name == "postgres" || seen[name] {
				continue
			}
			seen[name] = true
			matched++
			attr, ok := s.Attributes[name]
			if !ok {
				t.Errorf("plan_modifiers.jq references .name == %q, but the generated schema has no such attribute (renamed/removed => dead jq row)", name)
				continue
			}
			descs := wiring(attr)
			live := false
			for _, d := range descs {
				if injected(d) {
					live = true
					break
				}
			}
			if !live {
				t.Errorf("attribute %q is targeted by a %s row but carries no chain-injected %s (descs=%v): the row silently no-op'd, or a native %s masks a dead row", name, kind, kind, descs, kind)
			}
		}
		if matched == 0 {
			t.Errorf("no attribute-name branches found in the %s block; the regex or file structure changed", kind)
		}
	}

	descParentRef := planmodifiers.ParentRefStability().Description(ctx)
	injectedPM := func(d string) bool {
		return d == descUseStateForUnknown || d == descRequiresReplace || d == descParentRef
	}
	// Exact Description() matches: a "parent" substring would also match a retargeted
	// MatchRoot("parent_id") or the codegen's native OneOf, hiding a dead row.
	descConflictsParent := stringvalidator.ConflictsWith(path.MatchRoot("parent")).Description(ctx)
	descAlsoRequiresParent := stringvalidator.AlsoRequires(path.MatchRoot("parent")).Description(ctx)
	descRFC3339 := validators.RFC3339().Description(ctx)
	injectedVal := func(d string) bool {
		return d == descConflictsParent || d == descAlsoRequiresParent || d == descRFC3339
	}

	assertRows(blocks[1], "plan modifier", func(a schema.Attribute) []string { return postgresPlanModifierDescriptions(ctx, a) }, injectedPM)
	assertRows(blocks[2], "validator", func(a schema.Attribute) []string { return postgresValidatorDescriptions(ctx, a) }, injectedVal)
}
