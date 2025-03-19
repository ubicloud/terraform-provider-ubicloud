package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccLoadBalancerDataSource(t *testing.T) {
	resName := GetRandomResourceName("lb")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		PreCheck:                 func() { TestAccPreCheck(t) },
		Steps: []resource.TestStep{
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
        }
        
        data "ubicloud_load_balancer" "testacc" {
          project_id = ubicloud_load_balancer.testacc.project_id
					location = ubicloud_load_balancer.testacc.location
					name = ubicloud_load_balancer.testacc.name
        }`, GetTestAccProjectId(), resName, GetTestAccPrivateSubnetId()),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.ubicloud_load_balancer.testacc", "id"),
					resource.TestCheckResourceAttr("data.ubicloud_load_balancer.testacc", "project_id", GetTestAccProjectId()),
					resource.TestCheckResourceAttrSet("data.ubicloud_load_balancer.testacc", "location"),
					resource.TestCheckResourceAttr("data.ubicloud_load_balancer.testacc", "name", resName),
					resource.TestCheckResourceAttr("data.ubicloud_load_balancer.testacc", "src_port", "80"),
					resource.TestCheckResourceAttr("data.ubicloud_load_balancer.testacc", "dst_port", "80"),
					resource.TestCheckResourceAttr("data.ubicloud_load_balancer.testacc", "algorithm", "round_robin"),
					resource.TestCheckResourceAttr("data.ubicloud_load_balancer.testacc", "health_check_endpoint", "/up"),
					resource.TestCheckResourceAttr("data.ubicloud_load_balancer.testacc", "vms.#", "0"),
				),
			},
		},
	})
}
