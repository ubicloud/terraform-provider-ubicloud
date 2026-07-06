package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"
)

func boolRaw(b bool) tftypes.Value { return tftypes.NewValue(tftypes.Bool, b) }

// driveModifyPlanConfig takes SEPARATE config and plan values (driveModifyPlan wires
// Config == Plan), so a test can express config-null-but-plan-pinned shapes like the phantom.
func driveModifyPlanConfig(t *testing.T, ctx context.Context, configOver, stateOver, planOver map[string]tftypes.Value) *resource.ModifyPlanResponse {
	t.Helper()
	schema := resource_postgres.PostgresResourceSchema(ctx)
	configRaw := mkPGRaw(t, ctx, configOver)
	stateRaw := mkPGRaw(t, ctx, stateOver)
	planRaw := mkPGRaw(t, ctx, planOver)
	req := resource.ModifyPlanRequest{
		Config: tfsdk.Config{Schema: schema, Raw: configRaw},
		State:  tfsdk.State{Schema: schema, Raw: stateRaw},
		Plan:   tfsdk.Plan{Schema: schema, Raw: planRaw},
	}
	resp := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: schema, Raw: planRaw}}
	(&postgresResource{}).ModifyPlan(ctx, req, resp)
	return resp
}
