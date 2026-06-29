package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccProjectResource(t *testing.T) {
	resourceConfig := `
        resource "ubicloud_project" "testacc" {
            name = "TerraformAccTest"
        }
        `

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Test Create and Read
			{
				Config: providerConfig + resourceConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("ubicloud_project.testacc", "id"),
					resource.TestCheckResourceAttr("ubicloud_project.testacc", "name", "TerraformAccTest"),
					resource.TestCheckResourceAttrSet("ubicloud_project.testacc", "credit"),
					resource.TestCheckResourceAttrSet("ubicloud_project.testacc", "discount"),
				),
			},
			// No-op re-plan: name's RequiresReplace and the id/credit/discount
			// UseStateForUnknown plan modifiers must leave an unchanged config with an
			// empty plan (no spurious replace, no churn).
			{
				Config:   providerConfig + resourceConfig,
				PlanOnly: true,
			},
			// Test ImportState
			{
				ResourceName:      "ubicloud_project.testacc",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}
