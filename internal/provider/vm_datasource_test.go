package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccVmDataSource(t *testing.T) {
	resName := GetRandomResourceName("vm")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		PreCheck:                 func() { TestAccPreCheck(t) },
		Steps: []resource.TestStep{
			{
				Config: providerConfig +
					fmt.Sprintf(`
        resource "ubicloud_vm" "testacc" {
          project_id  			= "%s"
          location    			= "%s"
          private_subnet_id	= "%s"
          name        		  = "%s"
          public_key  			= "the public key"
        }
        
        data "ubicloud_vm" "testacc" {
          project_id = ubicloud_vm.testacc.project_id
          location = ubicloud_vm.testacc.location
          name = ubicloud_vm.testacc.name
        }`, GetTestAccProjectId(), GetTestAccLocation(), GetTestAccPrivateSubnetId(), resName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.ubicloud_vm.testacc", "id"),
					resource.TestCheckResourceAttr("data.ubicloud_vm.testacc", "project_id", GetTestAccProjectId()),
					resource.TestCheckResourceAttr("data.ubicloud_vm.testacc", "location", GetTestAccLocation()),
					resource.TestCheckResourceAttr("data.ubicloud_vm.testacc", "name", resName),
					resource.TestCheckResourceAttr("data.ubicloud_vm.testacc", "size", "standard-2"),
					resource.TestCheckResourceAttrSet("data.ubicloud_vm.testacc", "state"),
					resource.TestCheckResourceAttrSet("data.ubicloud_vm.testacc", "storage_size_gib"),
					resource.TestCheckResourceAttrSet("data.ubicloud_vm.testacc", "unix_user"),
					resource.TestCheckResourceAttr("data.ubicloud_vm.testacc", "firewall_rules.#", "0"),
				),
			},
		},
	})
}
