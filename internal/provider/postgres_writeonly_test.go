package provider

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// rawUnknownConfigMap mirrors what the framework hands Create for an omitted
// Optional+Computed map (postgresRaw's null default would not reproduce the bug).
func rawUnknownConfigMap() tftypes.Value {
	return tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, tftypes.UnknownValue)
}

// An omitted-config create plans the maps unknown; Create must hydrate them from GET
// .../config (the non-empty server config proves the read-back won, not a blind reset).
func TestCreateHydratesOmittedConfig(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{detailState: "running", configBody: &ubicloud_client.PostgresConfig{
		PgConfig:        map[string]string{"max_connections": "100"},
		PgbouncerConfig: map[string]string{},
	}}
	r := newPostgresResourceWithRT(t, capRT)

	resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
		"size":             strRaw("m8gd.large"),
		"storage_size":     numRaw(64),
		"pg_config":        rawUnknownConfigMap(),
		"pgbouncer_config": rawUnknownConfigMap(),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}

	var sawConfigGet bool
	for _, rq := range capRT.reqs {
		if rq.Method == http.MethodGet && strings.HasSuffix(rq.Path, "/config") {
			sawConfigGet = true
		}
	}
	if !sawConfigGet {
		t.Fatalf("Create must issue a GET .../config to hydrate omitted config; calls=%+v", capRT.reqs)
	}

	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if out.PgConfig.IsUnknown() {
		t.Fatalf("pg_config still unknown after create; Create must hydrate it to a known value")
	}
	if out.PgbouncerConfig.IsUnknown() {
		t.Fatalf("pgbouncer_config still unknown after create; Create must hydrate it to a known value")
	}
	pg := map[string]string{}
	out.PgConfig.ElementsAs(ctx, &pg, false)
	if pg["max_connections"] != "100" || len(pg) != 1 {
		t.Errorf("pg_config = %v, want {max_connections:100} hydrated from GET .../config", pg)
	}
	pgb := map[string]string{}
	out.PgbouncerConfig.ElementsAs(ctx, &pgb, false)
	if len(pgb) != 0 {
		t.Errorf("pgbouncer_config = %v, want empty (server has no overrides)", pgb)
	}
}

// When best-effort hydration is skipped (config GET 500s at creating-state), Create must
// default a still-unknown map to empty ({} is the server's user_config when nothing was sent).
func TestCreateConfigFallbackOnSkippedHydration(t *testing.T) {
	ctx := t.Context()
	capRT := &captureRT{detailState: "running", configStatus: http.StatusInternalServerError}
	r := newPostgresResourceWithRT(t, capRT)

	resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
		"size":             strRaw("m8gd.large"),
		"storage_size":     numRaw(64),
		"pg_config":        rawUnknownConfigMap(),
		"pgbouncer_config": rawUnknownConfigMap(),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("a skipped config read must not fail Create: %+v", resp.Diagnostics)
	}

	var out resource_postgres.PostgresModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("state get: %+v", diags)
	}
	if out.PgConfig.IsUnknown() || out.PgbouncerConfig.IsUnknown() {
		t.Fatalf("config maps must be known after create even when GET .../config is skipped; pg=%v pgb=%v",
			out.PgConfig, out.PgbouncerConfig)
	}
	if n := len(out.PgConfig.Elements()); n != 0 {
		t.Errorf("pg_config has %d elements, want empty fallback when hydration is skipped", n)
	}
	if n := len(out.PgbouncerConfig.Elements()); n != 0 {
		t.Errorf("pgbouncer_config has %d elements, want empty fallback when hydration is skipped", n)
	}
}

// restrict_by_default and private_subnet_name are write-only (no read surface echoes them);
// Optional+Computed would leave an omitted input unknown after apply, so Optional-only.
func TestPostgresWriteOnlyInputsAreOptionalOnly(t *testing.T) {
	ctx := t.Context()
	schema := resource_postgres.PostgresResourceSchema(ctx)
	for _, name := range []string{"restrict_by_default", "private_subnet_name"} {
		attr, ok := schema.Attributes[name]
		if !ok {
			t.Fatalf("postgres schema is missing attribute %q", name)
		}
		if !attr.IsOptional() {
			t.Errorf("%s must be Optional", name)
		}
		if attr.IsComputed() {
			t.Errorf("%s must NOT be Computed: it is write-only (never read back), so Computed leaves it unknown-after-apply when omitted", name)
		}
	}
}

func TestCreateBodyIncludesWriteOnlyInputs(t *testing.T) {
	ctx := t.Context()
	r, capRT := newCapturingPostgresResource(t)
	capRT.detailState = "running" // Create blocks until the detail GET reports running
	resp := driveCreate(t, ctx, r, map[string]tftypes.Value{
		"size":                strRaw("m8gd.large"),
		"storage_size":        numRaw(128),
		"flavor":              strRaw("standard"),
		"restrict_by_default": tftypes.NewValue(tftypes.Bool, true),
		"private_subnet_name": strRaw("tf-acc-ps"),
		"tags":                rawTags(t, ctx, [][2]string{{"env", "test"}}),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}

	var create *capturedReq
	for i := range capRT.reqs {
		rq := capRT.reqs[i]
		if rq.Method == http.MethodPost && strings.HasSuffix(rq.Path, "/postgres/tf-acc-pg") {
			create = &capRT.reqs[i]
		}
	}
	if create == nil {
		t.Fatalf("expected a create POST .../postgres/tf-acc-pg; calls=%+v", capRT.reqs)
	}
	b := bodyJSON(t, create.Body)
	if b["flavor"] != "standard" {
		t.Errorf("create body flavor = %v, want standard: %s", b["flavor"], create.Body)
	}
	if b["restrict_by_default"] != true {
		t.Errorf("create body restrict_by_default = %v, want true: %s", b["restrict_by_default"], create.Body)
	}
	if b["private_subnet_name"] != "tf-acc-ps" {
		t.Errorf("create body private_subnet_name = %v, want tf-acc-ps: %s", b["private_subnet_name"], create.Body)
	}
	tags, ok := b["tags"].([]any)
	if !ok || len(tags) != 1 {
		t.Fatalf("create body tags = %v, want one tag: %s", b["tags"], create.Body)
	}
	tag, ok := tags[0].(map[string]any)
	if !ok || tag["key"] != "env" || tag["value"] != "test" {
		t.Errorf("create body tag = %v, want {key:env,value:test}: %s", tags[0], create.Body)
	}
}
