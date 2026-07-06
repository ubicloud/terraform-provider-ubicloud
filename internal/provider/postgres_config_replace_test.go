package provider

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func numRaw(n int64) tftypes.Value { return tftypes.NewValue(tftypes.Number, big.NewFloat(float64(n))) }

func driveRead(t *testing.T, ctx context.Context, r *postgresResource, stateOver map[string]tftypes.Value) *resource.ReadResponse {
	t.Helper()
	schema := resource_postgres.PostgresResourceSchema(ctx)
	stateRaw := mkPGRaw(t, ctx, stateOver)
	req := resource.ReadRequest{State: tfsdk.State{Schema: schema, Raw: stateRaw}}
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: schema, Raw: stateRaw}}
	r.Read(ctx, req, resp)
	return resp
}

func driveCreate(t *testing.T, ctx context.Context, r *postgresResource, planOver map[string]tftypes.Value) *resource.CreateResponse {
	t.Helper()
	schema := resource_postgres.PostgresResourceSchema(ctx)
	planRaw := mkPGRaw(t, ctx, planOver)
	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schema, Raw: planRaw}}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schema, Raw: planRaw}}
	r.Create(ctx, req, resp)
	return resp
}

// withShortCreateTimeout lowers the create-timeout default for one test: the timeouts
// custom type exposes no settable attributes through the generated test schema.
func withShortCreateTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := postgresCreateTimeoutDefault
	postgresCreateTimeoutDefault = d
	t.Cleanup(func() { postgresCreateTimeoutDefault = prev })
}

func withFastAdoptBudget(t *testing.T, d time.Duration) {
	t.Helper()
	prev := postgresAdoptLookupBudget
	postgresAdoptLookupBudget = d
	t.Cleanup(func() { postgresAdoptLookupBudget = prev })
}
