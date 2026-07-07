package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"
)

// A terraform_data replaced each apply exposes a computed id unknown at plan; a conditional on
// it makes version unknown at plan (terraform does not fold `unknown ? "16" : "16"`) yet resolve
// to "16" at apply. This is the interpolated shape (version = module.x.major) that
// UseStateForUnknown no-ops on: version renders "known after apply", not the stale value.
func pgInterpolatedPrimaryConfig(name, gen string) string {
	return providerConfig + `
resource "terraform_data" "trig" { triggers_replace = "` + gen + `" }
locals { v = terraform_data.trig.id == "" ? "16" : "16" }
resource "ubicloud_postgres" "cb" {
  project_id       = "pjaaaaaaaaaaaaaaaaaaaaaaaa"
  location         = "aws-us-east-1"
  name             = "` + name + `"
  size             = "m8gd.large"
  storage_size     = 118
  version          = local.v
  ha_type          = "none"
  tags             = [{ key = "team", value = "data" }]
  pg_config        = {}
  pgbouncer_config = {}
}`
}

// TestAccPostgresInterpolatedStaleVersionRejectedAtApply pins that an interpolated version,
// unknown at the initial plan and pinned to the lagging major during a pending upgrade, is
// rejected at APPLY by the existing ModifyPlan guard, not a separate Update backstop: terraform
// re-plans at apply with the resolved config through ModifyPlan (PlanResourceChange precedes
// ApplyResourceChange), so "Postgres version upgrade pending" (a ModifyPlan-only message) fires
// there. The plancheck asserts the value is unknown at the initial plan. A framework change that
// stopped re-planning at apply would regress this to a silent stale-pin trap.
func TestAccPostgresInterpolatedStaleVersionRejectedAtApply(t *testing.T) {
	skipUnlessTFAcc(t)
	withFastPoll(t)
	var version, target atomic.Pointer[string]
	v16, v17 := "16", "17"
	version.Store(&v16)
	target.Store(&v16)
	srv := pgVersionFakeBackend(&version, &target)
	defer srv.Close()
	t.Setenv("UBICLOUD_API_ENDPOINT", srv.URL)
	t.Setenv("UBICLOUD_API_TOKEN", "pat-test")

	name := GetRandomResourceName("pgint")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: pgInterpolatedPrimaryConfig(name, "a")},
			{
				PreConfig: func() { target.Store(&v17) },
				Config:    pgInterpolatedPrimaryConfig(name, "b"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectUnknownValue("ubicloud_postgres.cb", tfjsonpath.New("version")),
					},
				},
				ExpectError: regexp.MustCompile(`Postgres version upgrade pending`),
			},
		},
	})
}

// pgReplicaVersionFakeBackend serves a read replica: the read-replica create echoes the child
// name from the request body, detail GETs report ReadReplica=true with the served version, and a
// DELETE marks the row gone so the -refresh=false teardown converges. It counts the maintenance
// window POST into windowPosts so a test can assert the mutation never landed.
func pgReplicaVersionFakeBackend(version *atomic.Pointer[string], windowPosts *atomic.Int32) *httptest.Server {
	var deleted atomic.Bool
	body := func(name string) ubicloud_client.PostgresDatabase {
		b := pgBodyForState(postgresStateRunning)
		b.Name = name
		b.ReadReplica = true
		b.Version = ubicloud_client.PostgresDatabaseVersion(*version.Load())
		b.TargetVersion = ubicloud_client.PostgresDatabaseTargetVersion(*version.Load())
		return b
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/set-maintenance-window"):
			windowPosts.Add(1)
			writePGJSON(w, http.StatusOK, body(pgNameFromPath(r.URL.Path)))
		case strings.HasSuffix(r.URL.Path, "/config"):
			writePGJSON(w, http.StatusOK, ubicloud_client.PostgresConfig{PgConfig: map[string]string{}, PgbouncerConfig: map[string]string{}})
		case strings.HasSuffix(r.URL.Path, "/read-replica"):
			var rb struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&rb)
			writePGJSON(w, http.StatusOK, body(rb.Name))
		default:
			if deleted.Load() {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
				return
			}
			writePGJSON(w, http.StatusOK, body(pgNameFromPath(r.URL.Path)))
		}
	}))
}

func pgInterpolatedReplicaConfig(name, gen, extra string) string {
	return providerConfig + `
resource "terraform_data" "trig" { triggers_replace = "` + gen + `" }
locals { v = terraform_data.trig.id == "" ? "16" : "16" }
resource "ubicloud_postgres" "rr" {
  project_id = "pjaaaaaaaaaaaaaaaaaaaaaaaa"
  location   = "aws-us-east-1"
  name       = "` + name + `"
  parent     = "tf-acc-src"
` + extra + `
}`
}

// TestAccPostgresInterpolatedReplicaVersionRejectedAtApply pins the read-replica arm of the same
// mechanism. A replica gains an interpolated version resolving to the inherited value plus an
// allowed maintenance-window change: unknown at plan it slips the config-keyed replica arm, and a
// delta-keyed check would see no change since the resolved value equals prior. At apply the
// resolved value trips ModifyPlan's replica arm with the "version cannot be changed on a read
// replica" detail. Asserting the maintenance-window POST never reached the backend proves the
// apply-time re-plan aborted before Update ran (an ExpectError alone would also accept the error
// from a post-apply plan that ran after Update had already landed the window change).
func TestAccPostgresInterpolatedReplicaVersionRejectedAtApply(t *testing.T) {
	skipUnlessTFAcc(t)
	withFastPoll(t)
	var version atomic.Pointer[string]
	var windowPosts atomic.Int32
	v16 := "16"
	version.Store(&v16)
	srv := pgReplicaVersionFakeBackend(&version, &windowPosts)
	defer srv.Close()
	t.Setenv("UBICLOUD_API_ENDPOINT", srv.URL)
	t.Setenv("UBICLOUD_API_TOKEN", "pat-test")

	name := GetRandomResourceName("pgrr")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: pgInterpolatedReplicaConfig(name, "a", "")},
			{
				Config: pgInterpolatedReplicaConfig(name, "b", "  version                     = local.v\n  maintenance_window_start_at = 14"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectUnknownValue("ubicloud_postgres.rr", tfjsonpath.New("version")),
					},
				},
				ExpectError: regexp.MustCompile(`version cannot be changed on a read replica`),
			},
		},
	})
	if n := windowPosts.Load(); n != 0 {
		t.Fatalf("set-maintenance-window POST reached the backend %d time(s): Update mutated the replica before the guard fired", n)
	}
}
