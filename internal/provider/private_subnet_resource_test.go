package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccPrivateSubnetResource(t *testing.T) {
	resName := GetRandomResourceName("sn")
	resourceConfig := fmt.Sprintf(`
        resource "ubicloud_private_subnet" "testacc" {
          project_id  = "%s"
          location    = "%s"
          firewall_id = "%s"
          name        = "%s"
        }`, GetTestAccProjectId(), GetTestAccLocation(), GetTestAccFirewallId(), resName)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Test Create and Read
			{
				Config: providerConfig + resourceConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("ubicloud_private_subnet.testacc", "id"),
					resource.TestCheckResourceAttr("ubicloud_private_subnet.testacc", "project_id", GetTestAccProjectId()),
					resource.TestCheckResourceAttr("ubicloud_private_subnet.testacc", "location", GetTestAccLocation()),
					resource.TestCheckResourceAttr("ubicloud_private_subnet.testacc", "name", resName),
					resource.TestCheckResourceAttrSet("ubicloud_private_subnet.testacc", "state"),
					resource.TestCheckResourceAttr("ubicloud_private_subnet.testacc", "firewalls.#", "1"),
					resource.TestCheckResourceAttr("ubicloud_private_subnet.testacc", "firewalls.0.firewall_rules.#", "4"),
					resource.TestCheckResourceAttr("ubicloud_private_subnet.testacc", "nics.#", "0"),
				),
			},
			// No-op re-plan: the create-only RequiresReplace (including the write-only
			// firewall_id) and stable-computed UseStateForUnknown plan modifiers must leave an
			// unchanged config with an empty plan. firewall_id is set but never read back, so
			// this proves RequiresReplace compares the configured value against state (equal)
			// rather than churning.
			{
				Config:   providerConfig + resourceConfig,
				PlanOnly: true,
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
