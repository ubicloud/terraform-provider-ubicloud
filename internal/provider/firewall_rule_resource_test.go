package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccFirewallRuleResource(t *testing.T) {
	resourceConfig := fmt.Sprintf(`
    resource "ubicloud_firewall" "testacc" {
      project_id  = "%s"
      location    = "%s"
      name        = "tf-testacc"
      description = "Terraform acceptance testing"
    }

    resource "ubicloud_firewall_rule" "testaccfwr1" {
      project_id  = ubicloud_firewall.testacc.project_id
      location    = ubicloud_firewall.testacc.location
      firewall_name = ubicloud_firewall.testacc.name
      cidr        = "0.0.0.0/0"
      port_range  = "22..23"
      description = "ssh access"
      protocol    = "tcp"
    }
    `, GetTestAccProjectId(), GetTestAccLocation())

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
					resource.TestCheckResourceAttr("ubicloud_firewall.testacc", "location", GetTestAccLocation()),
					resource.TestCheckResourceAttr("ubicloud_firewall.testacc", "name", "tf-testacc"),
					resource.TestCheckResourceAttr("ubicloud_firewall.testacc", "description", "Terraform acceptance testing"),
					resource.TestCheckResourceAttr("ubicloud_firewall.testacc", "firewall_rules.#", "1"),

					resource.TestCheckResourceAttrSet("ubicloud_firewall_rule.testaccfwr1", "id"),
					resource.TestCheckResourceAttr("ubicloud_firewall_rule.testaccfwr1", "project_id", GetTestAccProjectId()),
					resource.TestCheckResourceAttrSet("ubicloud_firewall_rule.testaccfwr1", "firewall_name"),
					resource.TestCheckResourceAttr("ubicloud_firewall_rule.testaccfwr1", "cidr", "0.0.0.0/0"),
					resource.TestCheckResourceAttr("ubicloud_firewall_rule.testaccfwr1", "port_range", "22..23"),
					resource.TestCheckResourceAttr("ubicloud_firewall_rule.testaccfwr1", "description", "ssh access"),
					resource.TestCheckResourceAttr("ubicloud_firewall_rule.testaccfwr1", "protocol", "tcp"),
				),
			},
			// No-op re-plan: the create-only RequiresReplace and stable-computed
			// UseStateForUnknown plan modifiers must leave an unchanged config with an empty
			// plan. This config omits firewall_id (referencing by firewall_name), so it proves
			// the Optional-only firewall reference does not plan a spurious replace on a no-op.
			{
				Config:   providerConfig + resourceConfig,
				PlanOnly: true,
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

// TestAccFirewallRuleResourceById exercises the firewall_id reference path end to end:
// create/read/import a rule that addresses its parent firewall by firewall_id (a UBID) and
// omits firewall_name. It uses a pre-created firewall (UBICLOUD_ACC_TEST_FIREWALL) like the
// private_subnet acc tests, so it does not depend on the ubicloud_firewall resource.
func TestAccFirewallRuleResourceById(t *testing.T) {
	firewallId := GetTestAccFirewallId()
	if firewallId == "" {
		t.Skip("UBICLOUD_ACC_TEST_FIREWALL must be set (a firewall UBID) for the firewall_id path test")
	}

	resourceConfig := fmt.Sprintf(`
    resource "ubicloud_firewall_rule" "byid" {
      project_id  = "%s"
      location    = "%s"
      firewall_id = "%s"
      cidr        = "1.2.3.0/24"
      port_range  = "80..8080"
      description = "by id"
      protocol    = "tcp"
    }
    `, GetTestAccProjectId(), GetTestAccLocation(), firewallId)

	importByIdFunc := func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources["ubicloud_firewall_rule.byid"]
		if !ok {
			return "", fmt.Errorf("Not found: ubicloud_firewall_rule.byid")
		}
		return fmt.Sprintf("%s,%s,%s,%s", GetTestAccProjectId(), rs.Primary.Attributes["location"], rs.Primary.Attributes["firewall_id"], rs.Primary.ID), nil
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		PreCheck:                 func() { TestAccPreCheck(t) },
		Steps: []resource.TestStep{
			{Config: providerConfig + resourceConfig},
			{
				Config: providerConfig + resourceConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("ubicloud_firewall_rule.byid", "id"),
					resource.TestCheckResourceAttr("ubicloud_firewall_rule.byid", "firewall_id", firewallId),
					resource.TestCheckResourceAttr("ubicloud_firewall_rule.byid", "cidr", "1.2.3.0/24"),
					resource.TestCheckResourceAttr("ubicloud_firewall_rule.byid", "port_range", "80..8080"),
					resource.TestCheckResourceAttr("ubicloud_firewall_rule.byid", "description", "by id"),
					resource.TestCheckResourceAttr("ubicloud_firewall_rule.byid", "protocol", "tcp"),
				),
			},
			{
				ResourceName:      "ubicloud_firewall_rule.byid",
				ImportState:       true,
				ImportStateIdFunc: importByIdFunc,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccFirewallRuleResourceFanOutRejected proves the client-side fan-out guard: a cidr
// that is not a literal IPv4/IPv6 cidr (here a private-subnet-style reference) is refused
// before any API call, so terraform never tracks a partial result. No backend resource is
// created.
func TestAccFirewallRuleResourceFanOutRejected(t *testing.T) {
	resourceConfig := fmt.Sprintf(`
    resource "ubicloud_firewall_rule" "fanout" {
      project_id    = "%s"
      location      = "%s"
      firewall_name = "tf-acc-nonexistent"
      cidr          = "tf-acc-private-subnet-ref"
    }
    `, GetTestAccProjectId(), GetTestAccLocation())

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		PreCheck:                 func() { TestAccPreCheck(t) },
		Steps: []resource.TestStep{
			{
				Config:      providerConfig + resourceConfig,
				ExpectError: regexp.MustCompile(`not a literal IPv4 or IPv6 CIDR`),
			},
		},
	})
}

// TestAccFirewallRulePortRangeCollapse proves the port_range custom type (semantic
// equality). The backend stores a firewall rule port range as a Postgres int4range and
// serializes an equal-bounds range collapsed to a single port (model/firewall_rule.rb
// display_port_range), so a user-written "22..22" round-trips as "22". Without semantic
// equality the create apply fails "Provider produced inconsistent result after apply"
// because the response "22" does not match the planned "22..22". PortRangeType treats the
// two forms as equal, so the framework retains the config form "22..22" in state on
// create and an identical re-plan is empty.
func TestAccFirewallRulePortRangeCollapse(t *testing.T) {
	resourceConfig := fmt.Sprintf(`
    resource "ubicloud_firewall" "testaccprc" {
      project_id  = "%s"
      location    = "%s"
      name        = "tf-testacc-prc"
      description = "Terraform acceptance testing"
    }

    resource "ubicloud_firewall_rule" "collapse" {
      project_id    = ubicloud_firewall.testaccprc.project_id
      location      = ubicloud_firewall.testaccprc.location
      firewall_name = ubicloud_firewall.testaccprc.name
      cidr          = "0.0.0.0/0"
      port_range    = "22..22"
      description   = "ssh"
      protocol      = "tcp"
    }
    `, GetTestAccProjectId(), GetTestAccLocation())

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		PreCheck:                 func() { TestAccPreCheck(t) },
		Steps: []resource.TestStep{
			// Create: "22..22" applies cleanly. Semantic equality retains the config
			// form in state even though the API stores and returns "22".
			{
				Config: providerConfig + resourceConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("ubicloud_firewall_rule.collapse", "id"),
					resource.TestCheckResourceAttr("ubicloud_firewall_rule.collapse", "port_range", "22..22"),
				),
			},
			// Re-plan of the identical config is empty: state holds "22..22" and config
			// is "22..22", so there is no diff.
			{
				Config:   providerConfig + resourceConfig,
				PlanOnly: true,
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
		return fmt.Sprintf("%s,%s,%s,%s", GetTestAccProjectId(), rs.Primary.Attributes["location"], rs.Primary.Attributes["firewall_name"], rs.Primary.ID), nil
	}
}
