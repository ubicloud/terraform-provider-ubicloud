package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccPrivateSubnetResource(t *testing.T) {
	resName := GetRandomResourceName("sn")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Test Create and Read
			{
				Config: providerConfig +
					fmt.Sprintf(`
        resource "ubicloud_private_subnet" "testacc" {
          project_id  = "%s"
          location    = "%s"
          firewall_id = "%s"
          name        = "%s"
        }`, GetTestAccProjectId(), GetTestAccLocation(), GetTestAccFirewallId(), resName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("ubicloud_private_subnet.testacc", "id"),
					resource.TestCheckResourceAttr("ubicloud_private_subnet.testacc", "project_id", GetTestAccProjectId()),
					resource.TestCheckResourceAttr("ubicloud_private_subnet.testacc", "location", GetTestAccLocation()),
					resource.TestCheckResourceAttr("ubicloud_private_subnet.testacc", "name", resName),
					resource.TestCheckResourceAttr("ubicloud_private_subnet.testacc", "firewall_rules.#", "0"),
					resource.TestCheckResourceAttr("ubicloud_private_subnet.testacc", "nics.#", "0"),
				),
			},
			// Test ImportState
			{
				ResourceName: "ubicloud_private_subnet.testacc",
				ImportState:  true,
				ImportStateIdFunc: func(state *terraform.State) (string, error) {
					return fmt.Sprintf("%s,%s,%s", GetTestAccProjectId(), GetTestAccLocation(), resName), nil
				},
			},
		},
	})
}
