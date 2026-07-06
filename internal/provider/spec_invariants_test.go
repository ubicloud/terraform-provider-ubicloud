package provider

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"
	"gopkg.in/yaml.v3"
)

// The server sends maintenance_window_start_at: null until a window is set; oapi-codegen emits the
// *int the mappers need only while the spec keeps the field nullable. Guards future spec resyncs
// against upstream regressing the field to a plain int.

const openapiSpecPath = "../../config/ubicloud_openapi.yml"

func digMap(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		next, ok := m[k].(map[string]any)
		if !ok {
			return nil
		}
		m = next
	}
	return m
}

// detailedMaintenanceWindow resolves the effective schema by oapi-codegen's allOf rule:
// an inline overlay member that redeclares the property overrides the base $ref.
func detailedMaintenanceWindow(spec map[string]any) map[string]any {
	schema := digMap(spec, "components", "responses", "PostgresDatabase", "content", "application/json", "schema")
	members := []any{schema}
	if allOf, ok := schema["allOf"].([]any); ok {
		members = allOf
	}
	baseRef := ""
	for _, m := range members {
		member, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if mw, ok := digMap(member, "properties")["maintenance_window_start_at"].(map[string]any); ok {
			return mw
		}
		if ref, ok := member["$ref"].(string); ok {
			baseRef = ref
		}
	}
	if path, ok := strings.CutPrefix(baseRef, "#/"); ok {
		base := digMap(spec, append(strings.Split(path, "/"), "properties")...)
		if mw, ok := base["maintenance_window_start_at"].(map[string]any); ok {
			return mw
		}
	}
	return nil
}

func TestPostgresDetailedMaintenanceWindowNullable(t *testing.T) {
	data, err := os.ReadFile(openapiSpecPath)
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatalf("parse %s: %v", openapiSpecPath, err)
	}

	mw := detailedMaintenanceWindow(spec)
	if mw == nil {
		t.Fatalf("%s: detailed PostgresDatabase response no longer declares maintenance_window_start_at; the surface shifted (ubicloud PR #5806)", openapiSpecPath)
	}
	if nullable, _ := mw["nullable"].(bool); !nullable {
		t.Fatalf("%s: detailed maintenance_window_start_at is not nullable; oapi-codegen will emit plain int and the API null fails to unmarshal (ubicloud PR #5806)", openapiSpecPath)
	}
}

// wrapDetailedResponse builds the minimal spec shape detailedMaintenanceWindow navigates:
// the detailed response as allOf($ref to the base schema, inline overlay).
func wrapDetailedResponse(base, overlay map[string]any) map[string]any {
	return map[string]any{
		"components": map[string]any{
			"schemas": map[string]any{"PostgresDatabase": base},
			"responses": map[string]any{"PostgresDatabase": map[string]any{
				"content": map[string]any{"application/json": map[string]any{
					"schema": map[string]any{"allOf": []any{
						map[string]any{"$ref": "#/components/schemas/PostgresDatabase"},
						overlay,
					}},
				}},
			}},
		},
	}
}

func TestDetailedMaintenanceWindowResolution(t *testing.T) {
	nullableMW := map[string]any{"type": "integer", "nullable": true}
	plainMW := map[string]any{"type": "integer"}
	base := map[string]any{"properties": map[string]any{"maintenance_window_start_at": nullableMW}}
	props := func(mw map[string]any) map[string]any {
		p := map[string]any{}
		if mw != nil {
			p["maintenance_window_start_at"] = mw
		}
		return map[string]any{"properties": p}
	}

	cases := []struct {
		name     string
		spec     map[string]any
		nullable bool
	}{
		{"inline overlay nullable", wrapDetailedResponse(base, props(nullableMW)), true},
		{"inline overlay drops nullable", wrapDetailedResponse(base, props(plainMW)), false},
		{"overlay omits, base nullable", wrapDetailedResponse(base, props(nil)), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mw := detailedMaintenanceWindow(tc.spec)
			if mw == nil {
				t.Fatal("resolved nil")
			}
			if got, _ := mw["nullable"].(bool); got != tc.nullable {
				t.Fatalf("effective nullable = %v, want %v", got, tc.nullable)
			}
		})
	}
}

func TestPostgresDetailedMaintenanceWindowGeneratedPointer(t *testing.T) {
	f, ok := reflect.TypeOf(ubicloud_client.PostgresDatabase{}).FieldByName("MaintenanceWindowStartAt")
	if !ok {
		t.Fatal("ubicloud_client.PostgresDatabase has no MaintenanceWindowStartAt field; the detailed response surface shifted (ubicloud PR #5806)")
	}
	if f.Type.Kind() != reflect.Pointer || f.Type.Elem().Kind() != reflect.Int {
		t.Fatalf("ubicloud_client.PostgresDatabase.MaintenanceWindowStartAt is %s, want *int; the detailed maintenance_window_start_at lost its nullable (ubicloud PR #5806)", f.Type)
	}
}
