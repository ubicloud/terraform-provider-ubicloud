package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccFirewallDataSource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		PreCheck:                 func() { TestAccPreCheck(t) },
		Steps: []resource.TestStep{
			{
				Config: providerConfig +
					fmt.Sprintf(`
				resource "ubicloud_firewall" "testacc" {
					project_id  = "%s"
					name        = "tf-testacc"
					description = "Terraform acceptance testing"
				}
				
				data "ubicloud_firewall" "testacc" {
				  project_id = ubicloud_firewall.testacc.project_id
					id = ubicloud_firewall.testacc.id
				}`, GetTestAccProjectId()),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.ubicloud_firewall.testacc", "id"),
					resource.TestCheckResourceAttr("data.ubicloud_firewall.testacc", "project_id", GetTestAccProjectId()),
					resource.TestCheckResourceAttr("data.ubicloud_firewall.testacc", "name", "tf-testacc"),
					resource.TestCheckResourceAttr("data.ubicloud_firewall.testacc", "description", "Terraform acceptance testing"),
					resource.TestCheckResourceAttr("data.ubicloud_firewall.testacc", "firewall_rules.#", "0"),
				),
			},
		},
	})
}
