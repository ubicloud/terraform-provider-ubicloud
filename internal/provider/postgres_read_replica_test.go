package provider

import (
	"context"
	"testing"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

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
