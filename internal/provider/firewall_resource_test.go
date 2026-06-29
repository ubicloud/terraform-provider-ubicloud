package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccFirewallResource(t *testing.T) {
	resourceConfig := fmt.Sprintf(`
        resource "ubicloud_firewall" "testacc" {
          project_id  = "%s"
          location    = "%s"
          name        = "tf-testacc"
          description = "Terraform acceptance testing"
        }`, GetTestAccProjectId(), GetTestAccLocation())

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Test Create and Read
			{
				Config: providerConfig + resourceConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("ubicloud_firewall.testacc", "id"),
					resource.TestCheckResourceAttr("ubicloud_firewall.testacc", "project_id", GetTestAccProjectId()),
					resource.TestCheckResourceAttr("ubicloud_firewall.testacc", "location", GetTestAccLocation()),
					resource.TestCheckResourceAttr("ubicloud_firewall.testacc", "name", "tf-testacc"),
					resource.TestCheckResourceAttr("ubicloud_firewall.testacc", "description", "Terraform acceptance testing"),
					resource.TestCheckResourceAttr("ubicloud_firewall.testacc", "firewall_rules.#", "0"),
					// private_subnets is Computed; a firewall created through the API is
					// attached to no subnet, so it must read back as a known empty list.
					// Before the fix it stayed unknown and apply hard-failed.
					resource.TestCheckResourceAttr("ubicloud_firewall.testacc", "private_subnets.#", "0"),
				),
			},
			// No-op re-plan: the create-only RequiresReplace and stable-computed
			// UseStateForUnknown plan modifiers must leave an unchanged config with an empty
			// plan (no spurious replace, no churn).
			{
				Config:   providerConfig + resourceConfig,
				PlanOnly: true,
			},
			// Test ImportState
			{
				ResourceName: "ubicloud_firewall.testacc",
				ImportState:  true,
				ImportStateIdFunc: func(state *terraform.State) (string, error) {
					return fmt.Sprintf("%s,%s,%s", GetTestAccProjectId(), GetTestAccLocation(), "tf-testacc"), nil
				},
			},
		},
	})
}
