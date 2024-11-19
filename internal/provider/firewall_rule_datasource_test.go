package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccFirewallRuleDataSource(t *testing.T) {
	resourceConfig := fmt.Sprintf(`
    resource "ubicloud_firewall" "testacc" {
      project_id  = "%s"
			location    = "%s"
      name        = "tf-testacc"
      description = "Terraform acceptance testing"
    }

    resource "ubicloud_firewall_rule" "testaccfwr1" {
      project_id  = ubicloud_firewall.testacc.project_id
      firewall_id = ubicloud_firewall.testacc.id
      cidr        = "1.2.3.0/24"
      port_range  = "80..8080"
    }

    resource "ubicloud_firewall_rule" "testaccfwr2" {
      project_id  = ubicloud_firewall.testacc.project_id
      firewall_id = ubicloud_firewall.testacc.id
      cidr        = "0.0.0.0/0"
      port_range  = "22..22"
    }			
    `, GetTestAccProjectId(), GetTestAccLocation())

	dataConfig := `
    data "ubicloud_firewall_rule" "testaccfwr1" {
      project_id = ubicloud_firewall.testacc.project_id
			location   = ubicloud_firewall.testacc.location
      firewall_id = ubicloud_firewall.testacc.id
      id = ubicloud_firewall_rule.testaccfwr1.id
    }

    data "ubicloud_firewall_rule" "testaccfwr2" {
      project_id = ubicloud_firewall.testacc.project_id
			location   = ubicloud_firewall.testacc.location
      firewall_id = ubicloud_firewall.testacc.id
      id = ubicloud_firewall_rule.testaccfwr2.id
    }
      
    data "ubicloud_firewall" "testacc" {
      project_id = ubicloud_firewall.testacc.project_id
			location   = ubicloud_firewall.testacc.location
      id = ubicloud_firewall.testacc.id
    }
    `

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		PreCheck:                 func() { TestAccPreCheck(t) },
		Steps: []resource.TestStep{
			{
				Config: providerConfig + resourceConfig,
			},
			{
				Config: providerConfig + resourceConfig + dataConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.ubicloud_firewall.testacc", "id"),
					resource.TestCheckResourceAttr("data.ubicloud_firewall.testacc", "project_id", GetTestAccProjectId()),
					resource.TestCheckResourceAttr("data.ubicloud_firewall.testacc", "location", GetTestAccLocation()),
					resource.TestCheckResourceAttr("data.ubicloud_firewall.testacc", "name", "tf-testacc"),
					resource.TestCheckResourceAttr("data.ubicloud_firewall.testacc", "description", "Terraform acceptance testing"),
					resource.TestCheckResourceAttr("data.ubicloud_firewall.testacc", "firewall_rules.#", "2"),

					resource.TestCheckResourceAttrSet("data.ubicloud_firewall_rule.testaccfwr1", "id"),
					resource.TestCheckResourceAttr("data.ubicloud_firewall_rule.testaccfwr1", "project_id", GetTestAccProjectId()),
					resource.TestCheckResourceAttrSet("data.ubicloud_firewall_rule.testaccfwr1", "firewall_id"),
					resource.TestCheckResourceAttr("data.ubicloud_firewall_rule.testaccfwr1", "cidr", "1.2.3.0/24"),
					resource.TestCheckResourceAttr("data.ubicloud_firewall_rule.testaccfwr1", "port_range", "80..8080"),

					resource.TestCheckResourceAttrSet("data.ubicloud_firewall_rule.testaccfwr2", "id"),
					resource.TestCheckResourceAttr("data.ubicloud_firewall_rule.testaccfwr2", "project_id", GetTestAccProjectId()),
					resource.TestCheckResourceAttrSet("data.ubicloud_firewall_rule.testaccfwr2", "firewall_id"),
					resource.TestCheckResourceAttr("data.ubicloud_firewall_rule.testaccfwr2", "cidr", "0.0.0.0/0"),
					resource.TestCheckResourceAttr("data.ubicloud_firewall_rule.testaccfwr2", "port_range", "22..22"),
				),
			},
		},
	})
}
