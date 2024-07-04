package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccPrivateSubnetDataSource(t *testing.T) {
	resName := GetRandomResourceName("sn")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		PreCheck:                 func() { TestAccPreCheck(t) },
		Steps: []resource.TestStep{
			{
				Config: providerConfig +
					fmt.Sprintf(`
				resource "ubicloud_private_subnet" "testacc" {
					project_id  = "%s"
					location    = "%s"
					firewall_id = "%s"
					name        = "%s"
				}
				
				data "ubicloud_private_subnet" "testacc" {
				  project_id = ubicloud_private_subnet.testacc.project_id
					location = ubicloud_private_subnet.testacc.location
					name = ubicloud_private_subnet.testacc.name
				}`, GetTestAccProjectId(), GetTestAccLocation(), GetTestAccFirewallId(), resName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.ubicloud_private_subnet.testacc", "id"),
					resource.TestCheckResourceAttr("data.ubicloud_private_subnet.testacc", "project_id", GetTestAccProjectId()),
					resource.TestCheckResourceAttr("data.ubicloud_private_subnet.testacc", "location", GetTestAccLocation()),
					resource.TestCheckResourceAttr("data.ubicloud_private_subnet.testacc", "name", resName),
					resource.TestCheckResourceAttr("data.ubicloud_private_subnet.testacc", "firewall_rules.#", "0"),
					resource.TestCheckResourceAttr("data.ubicloud_private_subnet.testacc", "nics.#", "0"),
				),
			},
		},
	})
}
