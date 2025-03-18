package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccPostgresResource(t *testing.T) {
	resName := GetRandomResourceName("pg")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Test Create and Read
			{
				Config: providerConfig +
					fmt.Sprintf(`
        resource "ubicloud_postgres" "testacc" {
          project_id  = "%s"
          location    = "%s"
          name        = "%s"
          size        = "standard-2"
		  version     = "17"
        }`, GetTestAccProjectId(), GetTestAccLocation(), resName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("ubicloud_postgres.testacc", "id"),
					resource.TestCheckResourceAttr("ubicloud_postgres.testacc", "project_id", GetTestAccProjectId()),
					resource.TestCheckResourceAttr("ubicloud_postgres.testacc", "location", GetTestAccLocation()),
					resource.TestCheckResourceAttr("ubicloud_postgres.testacc", "name", resName),
					resource.TestCheckResourceAttr("ubicloud_postgres.testacc", "vm_size", "standard-2"),
					resource.TestCheckResourceAttr("ubicloud_postgres.testacc", "ha_type", "none"),
					resource.TestCheckResourceAttr("ubicloud_postgres.testacc", "version", "17"),
					resource.TestCheckResourceAttr("ubicloud_postgres.testacc", "primary", "true"),
					resource.TestCheckResourceAttr("ubicloud_postgres.testacc", "firewall_rules.#", "1"),
					resource.TestCheckResourceAttr("ubicloud_postgres.testacc", "firewall_rules.0.cidr", "0.0.0.0/0"),
					resource.TestCheckResourceAttrSet("ubicloud_postgres.testacc", "storage_size_gib"),
				),
			},
			// Test ImportState
			{
				ResourceName: "ubicloud_postgres.testacc",
				ImportState:  true,
				ImportStateIdFunc: func(state *terraform.State) (string, error) {
					return fmt.Sprintf("%s,%s,%s", GetTestAccProjectId(), GetTestAccLocation(), resName), nil
				},
				ImportStateVerify: true,
			},
		},
	})
}
