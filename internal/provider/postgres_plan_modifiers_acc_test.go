package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// A live resize is nightly-cost and terraform-plugin-testing forbids PreApply plan checks
// on PlanOnly steps, so resize/replace plans stay unit-level; this asserts create + no-op re-plan.
func TestAccPostgresPlanModifiers(t *testing.T) {
	resName := GetRandomResourceName("pgmod")
	addr := "ubicloud_postgres.testpg"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: pgConfigNullTagsConfig(resName, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(addr, "id"),
					resource.TestCheckResourceAttr(addr, "name", resName),
					resource.TestCheckResourceAttr(addr, "flavor", "standard"),
					resource.TestCheckResourceAttrSet(addr, "created_at"),
				),
			},
			{
				Config:   pgConfigNullTagsConfig(resName, false),
				PlanOnly: true,
			},
		},
	})
}
