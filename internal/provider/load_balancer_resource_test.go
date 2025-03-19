package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccLoadBalancerResource(t *testing.T) {
	resName := GetRandomResourceName("lb")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Test Create and Read
			{
				Config: providerConfig +
					fmt.Sprintf(`
        resource "ubicloud_load_balancer" "testacc" {
          project_id  					= "%s"
          name        					= "%s"
					src_port							= 80
					dst_port							= 80
					algorithm							= "round_robin"
					private_subnet_id 		= "%s"
					health_check_endpoint = "/up"
					vms 									= []
        }`, GetTestAccProjectId(), resName, GetTestAccPrivateSubnetId()),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("ubicloud_load_balancer.testacc", "id"),
					resource.TestCheckResourceAttr("ubicloud_load_balancer.testacc", "project_id", GetTestAccProjectId()),
					resource.TestCheckResourceAttr("ubicloud_load_balancer.testacc", "name", resName),
					resource.TestCheckResourceAttr("ubicloud_load_balancer.testacc", "src_port", "80"),
					resource.TestCheckResourceAttr("ubicloud_load_balancer.testacc", "dst_port", "80"),
					resource.TestCheckResourceAttr("ubicloud_load_balancer.testacc", "algorithm", "round_robin"),
					resource.TestCheckResourceAttr("ubicloud_load_balancer.testacc", "health_check_endpoint", "/up"),
					resource.TestCheckResourceAttr("ubicloud_load_balancer.testacc", "vms.#", "0"),
					resource.TestCheckResourceAttrSet("ubicloud_load_balancer.testacc", "hostname"),
				),
			},
			// Test ImportState
			{
				ResourceName: "ubicloud_load_balancer.testacc",
				ImportState:  true,
				ImportStateIdFunc: func(state *terraform.State) (string, error) {
					return fmt.Sprintf("%s,%s,%s", GetTestAccProjectId(), GetTestAccLocation(), resName), nil
				},
				ImportStateVerify: true,
			},
		},
	})
}
