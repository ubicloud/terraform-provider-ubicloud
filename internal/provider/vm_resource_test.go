package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccVmResource(t *testing.T) {
	resName := GetRandomResourceName("vm")
	resourceConfig := fmt.Sprintf(`
  resource "ubicloud_vm" "testacc" {
    project_id 				= "%s"
    location   				= "%s"
    private_subnet_id = "%s"
    name        			= "%s"
    public_key  			= "the public key"
    size							= "standard-2"
    storage_size 			= 40
  }`, GetTestAccProjectId(), GetTestAccLocation(), GetTestAccPrivateSubnetId(), resName)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Test Create and Read
			{
				Config: providerConfig + resourceConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("ubicloud_vm.testacc", "id"),
					resource.TestCheckResourceAttr("ubicloud_vm.testacc", "project_id", GetTestAccProjectId()),
					resource.TestCheckResourceAttr("ubicloud_vm.testacc", "location", GetTestAccLocation()),
					resource.TestCheckResourceAttr("ubicloud_vm.testacc", "name", resName),
					resource.TestCheckResourceAttr("ubicloud_vm.testacc", "size", "standard-2"),
					resource.TestCheckResourceAttrSet("ubicloud_vm.testacc", "storage_size_gib"),
					resource.TestCheckResourceAttrSet("ubicloud_vm.testacc", "unix_user"),
				),
			},
			// No-op: re-planning the identical config must be an EMPTY plan. The vm resource has
			// no in-place Update, so this proves the plan modifiers behave: UseStateForUnknown pins
			// the stable read-back computeds (id, ips, subnet, storage_size_gib, firewalls, size,
			// unix_user) so they do not churn as "(known after apply)", and every create-only
			// immutable compares equal so RequiresReplace fires no spurious replace. A non-empty
			// plan here fails the step.
			{
				Config:   providerConfig + resourceConfig,
				PlanOnly: true,
			},
			// Test ImportState
			{
				ResourceName: "ubicloud_vm.testacc",
				ImportState:  true,
				ImportStateIdFunc: func(state *terraform.State) (string, error) {
					return fmt.Sprintf("%s,%s,%s", GetTestAccProjectId(), GetTestAccLocation(), resName), nil
				},
			},
		},
	})
}
