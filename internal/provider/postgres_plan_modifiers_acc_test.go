package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// pgPlanModifierConfig renders a cloud (AWS) postgres config. The four write-only
// create inputs (restrict_by_default, private_subnet_name, pg_config,
// pgbouncer_config) are set explicitly so the create succeeds. The provider neither
// sends them in the create body nor reads them back from the response, so when left
// unset they stay unknown-after-apply (a separate, pre-existing create gap tracked as
// pg-writeonly-create-inputs-unknown). Setting them makes Create echo the configured
// values from plan into state, which is enough for a clean create to exercise the
// plan modifiers; it does NOT exercise a real API round-trip for those four fields.
func pgPlanModifierConfig(name string, storage int) string {
	return providerConfig + fmt.Sprintf(`
resource "ubicloud_postgres" "testpm" {
  project_id          = %q
  location            = %q
  name                = %q
  size                = "m8gd.large"
  storage_size        = %d
  version             = "17"
  ha_type             = "none"
  restrict_by_default = false
  private_subnet_name = "%s-ps"
  pg_config           = {}
  pgbouncer_config    = {}
}`, GetTestAccProjectId(), GetTestAccLocation(), name, storage, name)
}

// TestAccPostgresPlanModifiers proves the postgres plan modifiers and the reworked
// Update against a live clover with one cheap create (state=creating):
//
//   - the create succeeds and the resource round-trips (clean apply);
//   - re-planning the identical config is an empty plan: UseStateForUnknown stops the
//     invariant computeds churning as "(known after apply)";
//   - a PATCH-set change (storage_size) reaches Update and returns the clear
//     "not yet implemented" error instead of the old blanket "not supported".
//
// The RequiresReplace behaviour (flavor change plans as a replace, not an in-place
// update) is a plan-without-apply assertion; terraform-plugin-testing forbids
// PreApply plan checks on PlanOnly steps, so it is proven by the differential CLI
// proof in scripts/plan_modifier_proof.sh and by the schema-level unit test.
func TestAccPostgresPlanModifiers(t *testing.T) {
	resName := GetRandomResourceName("pgmod")
	addr := "ubicloud_postgres.testpm"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { TestAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create at state=creating.
			{
				Config: pgPlanModifierConfig(resName, 118),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(addr, "id"),
					resource.TestCheckResourceAttr(addr, "name", resName),
					resource.TestCheckResourceAttr(addr, "flavor", "standard"),
					resource.TestCheckResourceAttrSet(addr, "created_at"),
				),
			},
			// No-op: identical config re-plans to nothing (UseStateForUnknown).
			{
				Config:   pgPlanModifierConfig(resName, 118),
				PlanOnly: true,
			},
			// PATCH-set change reaches Update and returns the clear not-yet-implemented
			// error (the immutables never get here; they replace via RequiresReplace).
			{
				Config:      pgPlanModifierConfig(resName, 128),
				ExpectError: regexp.MustCompile(`In-place update of postgres is not yet implemented`),
			},
		},
	})
}
