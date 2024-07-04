package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccProjectDataSource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		PreCheck:                 func() { TestAccPreCheck(t) },
		Steps: []resource.TestStep{
			{
				Config: providerConfig +
					fmt.Sprintf(`
				data "ubicloud_project" "testacc" {
					id = "%s"
				}`, GetTestAccProjectId()),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.ubicloud_project.testacc", "id", GetTestAccProjectId()),
					resource.TestCheckResourceAttr("data.ubicloud_project.testacc", "name", "Terraform"),
					resource.TestCheckResourceAttr("data.ubicloud_project.testacc", "credit", "0"),
					resource.TestCheckResourceAttr("data.ubicloud_project.testacc", "discount", "100"),
				),
			},
		},
	})
}
