package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccPostgresDataSource(t *testing.T) {
	resName := GetRandomResourceName("pg")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		PreCheck:                 func() { TestAccPreCheck(t) },
		Steps: []resource.TestStep{
			{
				Config: providerConfig +
					fmt.Sprintf(`
				resource "ubicloud_postgres" "testacc" {
					project_id  = "%s"
					location    = "%s"
					name        = "%s"
					size       = "standard-2"
				}
				
				data "ubicloud_postgres" "testacc" {
				  project_id = ubicloud_postgres.testacc.project_id
					location = ubicloud_postgres.testacc.location
					name = ubicloud_postgres.testacc.name
				}`, GetTestAccProjectId(), GetTestAccLocation(), resName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.ubicloud_postgres.testacc", "id"),
					resource.TestCheckResourceAttr("data.ubicloud_postgres.testacc", "project_id", GetTestAccProjectId()),
					resource.TestCheckResourceAttr("data.ubicloud_postgres.testacc", "location", GetTestAccLocation()),
					resource.TestCheckResourceAttr("data.ubicloud_postgres.testacc", "name", resName),
					resource.TestCheckResourceAttr("data.ubicloud_postgres.testacc", "vm_size", "standard-2"),
					resource.TestCheckResourceAttr("data.ubicloud_postgres.testacc", "ha_type", "none"),
					resource.TestCheckResourceAttr("data.ubicloud_postgres.testacc", "primary", "true"),
					resource.TestCheckResourceAttr("data.ubicloud_postgres.testacc", "firewall_rules.#", "1"),
					resource.TestCheckResourceAttr("data.ubicloud_postgres.testacc", "firewall_rules.0.cidr", "0.0.0.0/0"),
					resource.TestCheckResourceAttrSet("data.ubicloud_postgres.testacc", "state"),
					resource.TestCheckResourceAttrSet("data.ubicloud_postgres.testacc", "storage_size_gib"),
				),
			},
		},
	})
}
