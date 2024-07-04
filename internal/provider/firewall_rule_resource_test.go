package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccFirewallRuleResource(t *testing.T) {
	resourceConfig := fmt.Sprintf(`
    resource "ubicloud_firewall" "testacc" {
      project_id  = "%s"
      name        = "tf-testacc"
      description = "Terraform acceptance testing"
    }

    resource "ubicloud_firewall_rule" "testaccfwr1" {
      project_id  = ubicloud_firewall.testacc.project_id
      firewall_id = ubicloud_firewall.testacc.id
      cidr        = "0.0.0.0/0"
      port_range  = "22..22"
    }			
    `, GetTestAccProjectId())

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		PreCheck:                 func() { TestAccPreCheck(t) },
		Steps: []resource.TestStep{
			// Test Create and Read
			{
				Config: providerConfig + resourceConfig,
			},
			{
				Config: providerConfig + resourceConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("ubicloud_firewall.testacc", "id"),
					resource.TestCheckResourceAttr("ubicloud_firewall.testacc", "project_id", GetTestAccProjectId()),
					resource.TestCheckResourceAttr("ubicloud_firewall.testacc", "name", "tf-testacc"),
					resource.TestCheckResourceAttr("ubicloud_firewall.testacc", "description", "Terraform acceptance testing"),
					resource.TestCheckResourceAttr("ubicloud_firewall.testacc", "firewall_rules.#", "1"),

					resource.TestCheckResourceAttrSet("ubicloud_firewall_rule.testaccfwr1", "id"),
					resource.TestCheckResourceAttr("ubicloud_firewall_rule.testaccfwr1", "project_id", GetTestAccProjectId()),
					resource.TestCheckResourceAttrSet("ubicloud_firewall_rule.testaccfwr1", "firewall_id"),
					resource.TestCheckResourceAttr("ubicloud_firewall_rule.testaccfwr1", "cidr", "0.0.0.0/0"),
					resource.TestCheckResourceAttr("ubicloud_firewall_rule.testaccfwr1", "port_range", "22..22"),
				),
			},
			// Test ImportState
			{
				ResourceName:      "ubicloud_firewall_rule.testaccfwr1",
				ImportState:       true,
				ImportStateIdFunc: importStateIdFunc("ubicloud_firewall_rule.testaccfwr1"),
				ImportStateVerify: true,
			},
		},
	})
}

func importStateIdFunc(fwr string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[fwr]

		if !ok {
			return "", fmt.Errorf("Not found: %s", fwr)
		}

		if rs.Primary.ID == "" {
			return "", fmt.Errorf("No Record ID is set")
		}
		return fmt.Sprintf("%s,%s,%s", GetTestAccProjectId(), rs.Primary.Attributes["firewall_id"], rs.Primary.ID), nil
	}
}
