package provider

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"
)

// pgVersionFakeBackend is a network-boundary fake whose served version and target_version are
// swapped through their pointers, so a test can simulate either a converged major upgrade (both
// advance) or one in flight (target ahead of version) landing out of band while the config still
// pins the old version. It otherwise mirrors pgReplaceFakeBackend: a create POST and every detail
// GET return a running snapshot echoing the requested name, GET .../config returns empty maps, and
// DELETE marks the row gone so a -refresh=false teardown converges.
func pgVersionFakeBackend(version, target *atomic.Pointer[string]) *httptest.Server {
	var deleted atomic.Bool
	body := func(name string) ubicloud_client.PostgresDatabase {
		b := pgBodyForState(postgresStateRunning)
		b.Name = name
		b.Version = ubicloud_client.PostgresDatabaseVersion(*version.Load())
		b.TargetVersion = ubicloud_client.PostgresDatabaseTargetVersion(*target.Load())
		return b
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/config"):
			writePGJSON(w, http.StatusOK, ubicloud_client.PostgresConfig{PgConfig: map[string]string{}, PgbouncerConfig: map[string]string{}})
		case r.Method == http.MethodPost:
			writePGJSON(w, http.StatusOK, body(pgNameFromPath(r.URL.Path)))
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

// pgVersionConfig renders a primary create pinned to 16, the lagging version both destroy
// pre-walk probes hold in config while the server version and target_version move out of band.
func pgVersionConfig(name string) string {
	return providerConfig + `
resource "ubicloud_postgres" "cb" {
  project_id       = "pjaaaaaaaaaaaaaaaaaaaaaaaa"
  location         = "aws-us-east-1"
  name             = "` + name + `"
  size             = "m8gd.large"
  storage_size     = 118
  version          = "16"
  ha_type          = "none"
  tags             = [{ key = "team", value = "data" }]
  pg_config        = {}
  pgbouncer_config = {}
}`
}

// TestAccPostgresDestroyBlockedByStaleVersion pins the destroy pre-walk against real terraform: a
// terraform destroy defaults to -refresh=true, which re-plans the configuration against refreshed
// state before deleting. With the server upgraded to 18 out of band and the config still pinned to
// 16, that pre-destroy plan is a rejected 18->16 downgrade and the ModifyPlan version guard blocks
// the destroy. This is the scope decision (keep the hard error, do not unblock the destroy); the
// guard detail must carry the stale-config hint so the user learns the escape. A framework or
// terraform change that stopped routing the destroy pre-walk through ModifyPlan would silently
// regress this to a passing destroy. The mandatory post-test teardown runs -refresh=false
// (wd.Destroy), which skips the drift plan, so it is not blocked.
func TestAccPostgresDestroyBlockedByStaleVersion(t *testing.T) {
	skipUnlessTFAcc(t)
	withFastPoll(t)
	var version, target atomic.Pointer[string]
	v16, v18 := "16", "18"
	version.Store(&v16)
	target.Store(&v16)
	srv := pgVersionFakeBackend(&version, &target)
	defer srv.Close()
	t.Setenv("UBICLOUD_API_ENDPOINT", srv.URL)
	t.Setenv("UBICLOUD_API_TOKEN", "pat-test")

	name := GetRandomResourceName("pgdst")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: pgVersionConfig(name)},
			{
				PreConfig:   func() { version.Store(&v18); target.Store(&v18) },
				Config:      pgVersionConfig(name),
				Destroy:     true,
				ExpectError: regexp.MustCompile(`(?s)Unsupported postgres version change.*-refresh=false`),
			},
		},
	})
}

// TestAccPostgresDestroyBlockedByInflightUpgrade pins the destroy pre-walk for the in-flight case:
// with an upgrade dispatched out of band (version still 16, target_version 17) and the config still
// pinned to 16, the -refresh=true pre-destroy plan reads a benign no-op that the in-flight guard
// converts into a hard error, matching the converged-downgrade decision (keep the error, name the
// escape). Without the guard this destroy would proceed silently, and the same config would later
// hard-error as a downgrade once the server flipped to 17. The post-test teardown runs
// -refresh=false, skipping the drift plan, so it still converges.
func TestAccPostgresDestroyBlockedByInflightUpgrade(t *testing.T) {
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

	name := GetRandomResourceName("pgdst")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: pgVersionConfig(name)},
			{
				PreConfig:   func() { target.Store(&v17) },
				Config:      pgVersionConfig(name),
				Destroy:     true,
				ExpectError: regexp.MustCompile(`(?s)Postgres version upgrade pending.*-refresh=false`),
			},
		},
	})
}
