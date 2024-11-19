package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccFirewallResource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Test Create and Read
			{
				Config: providerConfig +
					fmt.Sprintf(`
        resource "ubicloud_firewall" "testacc" {
          project_id  = "%s"
          location    = "%s"
          name        = "tf-testacc"
          description = "Terraform acceptance testing"
        }`, GetTestAccProjectId(), GetTestAccLocation()),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("ubicloud_firewall.testacc", "id"),
					resource.TestCheckResourceAttr("ubicloud_firewall.testacc", "project_id", GetTestAccProjectId()),
					resource.TestCheckResourceAttr("ubicloud_firewall.testacc", "location", GetTestAccLocation()),
					resource.TestCheckResourceAttr("ubicloud_firewall.testacc", "name", "tf-testacc"),
					resource.TestCheckResourceAttr("ubicloud_firewall.testacc", "description", "Terraform acceptance testing"),
					resource.TestCheckResourceAttr("ubicloud_firewall.testacc", "firewall_rules.#", "0"),
				),
			},
			// Test ImportState
			{
				ResourceName:        "ubicloud_firewall.testacc",
				ImportState:         true,
				ImportStateIdFunc: func(state *terraform.State) (string, error) {
					return fmt.Sprintf("%s,%s,%s", GetTestAccProjectId(), GetTestAccLocation(), "tf-testacc"), nil
				},
			},
		},
	})
}
