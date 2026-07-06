package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Uses the generic acceptance location/SKU so it runs under any UBICLOUD_ACC_TEST_LOCATION;
// every create input is pinned to a concrete literal so the identical re-plan settles to a no-op.
func pgPlanModifierConfig(name string, storage int) string {
	return providerConfig + fmt.Sprintf(`
resource "ubicloud_postgres" "testpm" {
  project_id          = %q
  location            = %q
  name                = %q
  size                = "standard-2"
  storage_size        = %d
  version             = "17"
  ha_type             = "none"
  restrict_by_default = false
  private_subnet_name = "%s-ps"
  pg_config           = {}
  pgbouncer_config    = {}
}`, GetTestAccProjectId(), GetTestAccLocation(), name, storage, name)
}

// A live resize is nightly-cost and terraform-plugin-testing forbids PreApply plan checks
// on PlanOnly steps, so resize/replace plans stay unit-level; this asserts create + no-op re-plan.
func TestAccPostgresPlanModifiers(t *testing.T) {
	resName := GetRandomResourceName("pgmod")
	addr := "ubicloud_postgres.testpm"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: pgPlanModifierConfig(resName, 64),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(addr, "id"),
					resource.TestCheckResourceAttr(addr, "name", resName),
					resource.TestCheckResourceAttr(addr, "flavor", "standard"),
					resource.TestCheckResourceAttrSet(addr, "created_at"),
				),
			},
			{
				Config:   pgPlanModifierConfig(resName, 64),
				PlanOnly: true,
			},
		},
	})
}
