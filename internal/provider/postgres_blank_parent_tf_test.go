package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// validators.NotBlank() on the parent attribute replaced the two ModifyPlan arms that rejected a
// blank parent. These probes drive REAL terraform to prove the validator fires at plan for a create
// and an update, and that (running at config validation, not ModifyPlan) it also blocks a destroy
// the removed update arm did not.

// A blank parent at create is rejected at plan by the validator. The config omits size, so were the
// validator not firing ModifyPlan would instead report "Missing size" (or the plan would succeed),
// which is why this step pins that the validator runs. ExpectError matches presence only, so it
// cannot assert the sole-error short-circuit; that terraform halts the plan at validation (no
// "Missing size" alongside) was confirmed by a one-off observation, not by this passing step.
func TestAccPostgresBlankParentRejectedAtCreate(t *testing.T) {
	skipUnlessTFAcc(t)
	srv := pgReplaceFakeBackend()
	defer srv.Close()
	t.Setenv("UBICLOUD_API_ENDPOINT", srv.URL)
	t.Setenv("UBICLOUD_API_TOKEN", "pat-test")

	cfg := providerConfig + `
resource "ubicloud_postgres" "bp" {
  project_id = "pjaaaaaaaaaaaaaaaaaaaaaaaa"
  location   = "aws-us-east-1"
  name       = "` + GetRandomResourceName("pgbp") + `"
  parent     = ""
}`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`Blank parent`),
			},
		},
	})
}

// A blank parent on an EXISTING primary is rejected at plan on update AND blocks a terraform
// destroy. Config validation runs on every operation, so unlike the removed ModifyPlan arm (which
// bailed on the null destroy plan) the validator fails the destroy pre-walk too; -refresh=false
// does not skip it. Only parent changes from the valid step-1 config. The final valid step lets the
// framework's -refresh=false teardown converge, since that teardown is itself blocked while the
// config holds a blank parent.
func TestAccPostgresBlankParentRejectedOnUpdateAndDestroy(t *testing.T) {
	skipUnlessTFAcc(t)
	withFastPoll(t)
	srv := pgReplaceFakeBackend()
	defer srv.Close()
	t.Setenv("UBICLOUD_API_ENDPOINT", srv.URL)
	t.Setenv("UBICLOUD_API_TOKEN", "pat-test")

	resName := GetRandomResourceName("pgbp")
	blankParentCfg := providerConfig + `
resource "ubicloud_postgres" "cb" {
  project_id       = "pjaaaaaaaaaaaaaaaaaaaaaaaa"
  location         = "aws-us-east-1"
  name             = "` + resName + `"
  parent           = ""
  size             = "m8gd.large"
  storage_size     = 118
  version          = "17"
  ha_type          = "none"
  tags             = [{ key = "team", value = "data" }]
  pg_config        = {}
  pgbouncer_config = {}
}`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: pgPrimaryConfig(resName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ubicloud_postgres.cb", "name", resName),
					resource.TestCheckResourceAttr("ubicloud_postgres.cb", "state", postgresStateRunning),
				),
			},
			{
				Config:      blankParentCfg,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`Blank parent`),
			},
			{
				Config:      blankParentCfg,
				Destroy:     true,
				ExpectError: regexp.MustCompile(`Blank parent`),
			},
			{
				Config: pgPrimaryConfig(resName),
			},
		},
	})
}
