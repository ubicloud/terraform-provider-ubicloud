package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// withTags=false omits the tags block entirely: the un-manage transition, identical at plan
// time to importing a tagged database with a config that omits tags.
func pgConfigNullTagsConfig(name string, storage int, withTags bool) string {
	tags := ""
	if withTags {
		tags = `
  tags = [
    { key = "team", value = "data" },
  ]`
	}
	return providerConfig + fmt.Sprintf(`
resource "ubicloud_postgres" "testtags" {
  project_id          = %q
  location            = %q
  name                = %q
  size                = "standard-2"
  storage_size        = %d
  version             = "17"
  ha_type             = "none"
  restrict_by_default = false
  private_subnet_name = "%s-ps"
  pg_config           = {}
  pgbouncer_config    = {}%s
}`, GetTestAccProjectId(), GetTestAccLocation(), name, storage, name, tags)
}

// Created WITH tags then un-managed: the server still reports them, so the core no-op gate
// would plan a perpetual phantom update; PlanOnly asserts ModifyPlan absorbs it to nothing.
func TestAccPostgresConfigNullTagsNoPhantom(t *testing.T) {
	resName := GetRandomResourceName("pgtags")
	addr := "ubicloud_postgres.testtags"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { TestAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: pgConfigNullTagsConfig(resName, 64, true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(addr, "id"),
					resource.TestCheckResourceAttr(addr, "name", resName),
					resource.TestCheckResourceAttr(addr, "tags.#", "1"),
				),
			},
			{
				Config:   pgConfigNullTagsConfig(resName, 64, false),
				PlanOnly: true,
			},
		},
	})
}
