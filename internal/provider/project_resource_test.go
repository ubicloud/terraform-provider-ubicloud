package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccProjectResource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Test Create and Read
			{
				Config: providerConfig + `
        resource "ubicloud_project" "testacc" {
            name = "TerraformAccTest"
        }
        `,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("ubicloud_project.testacc", "id"),
					resource.TestCheckResourceAttr("ubicloud_project.testacc", "name", "TerraformAccTest"),
					resource.TestCheckResourceAttrSet("ubicloud_project.testacc", "credit"),
					resource.TestCheckResourceAttrSet("ubicloud_project.testacc", "discount"),
				),
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
