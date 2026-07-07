package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccFirewallDataSource(t *testing.T) {
	resName := GetRandomResourceName("fw")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		PreCheck:                 func() { testAccPreCheck(t) },
		Steps: []resource.TestStep{
			{
				Config: providerConfig +
					fmt.Sprintf(`
        resource "ubicloud_firewall" "testacc" {
          project_id  = "%s"
          location    = "%s"
          name        = "%s"
          description = "Terraform acceptance testing"
        }

        data "ubicloud_firewall" "testacc" {
          project_id = ubicloud_firewall.testacc.project_id
          location   = ubicloud_firewall.testacc.location
          name       = ubicloud_firewall.testacc.name
        }`, GetTestAccProjectId(), GetTestAccLocation(), resName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.ubicloud_firewall.testacc", "id"),
					resource.TestCheckResourceAttr("data.ubicloud_firewall.testacc", "project_id", GetTestAccProjectId()),
					resource.TestCheckResourceAttr("data.ubicloud_firewall.testacc", "location", GetTestAccLocation()),
					resource.TestCheckResourceAttr("data.ubicloud_firewall.testacc", "name", resName),
					resource.TestCheckResourceAttr("data.ubicloud_firewall.testacc", "description", "Terraform acceptance testing"),
					resource.TestCheckResourceAttr("data.ubicloud_firewall.testacc", "firewall_rules.#", "0"),
				),
			},
		},
	})
}
