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

// pgVersionFakeBackend is a network-boundary fake whose served postgres version is swapped through
// the version pointer, so a test can simulate a major upgrade landing on the server out of band
// while the config still pins the old version. It otherwise mirrors pgReplaceFakeBackend: a create
// POST and every detail GET return a running snapshot echoing the requested name and current
// version, GET .../config returns empty maps, and DELETE marks the row gone so a -refresh=false
// teardown converges.
func pgVersionFakeBackend(version *atomic.Pointer[string]) *httptest.Server {
	var deleted atomic.Bool
	body := func(name string) ubicloud_client.PostgresDatabase {
		b := pgBodyForState(postgresStateRunning)
		b.Name = name
		b.Version = ubicloud_client.PostgresDatabaseVersion(*version.Load())
		b.TargetVersion = ubicloud_client.PostgresDatabaseTargetVersion(*version.Load())
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

// pgVersionConfig renders a primary create pinned to version, matching pgVersionFakeBackend's
// running snapshot so the create apply is plan-consistent.
func pgVersionConfig(name, version string) string {
	return providerConfig + `
resource "ubicloud_postgres" "cb" {
  project_id       = "pjaaaaaaaaaaaaaaaaaaaaaaaa"
  location         = "aws-us-east-1"
  name             = "` + name + `"
  size             = "m8gd.large"
  storage_size     = 118
  version          = "` + version + `"
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
	var version atomic.Pointer[string]
	v16, v18 := "16", "18"
	version.Store(&v16)
	srv := pgVersionFakeBackend(&version)
	defer srv.Close()
	t.Setenv("UBICLOUD_API_ENDPOINT", srv.URL)
	t.Setenv("UBICLOUD_API_TOKEN", "pat-test")

	name := GetRandomResourceName("pgdst")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: pgVersionConfig(name, "16")},
			{
				PreConfig:   func() { version.Store(&v18) },
				Config:      pgVersionConfig(name, "16"),
				Destroy:     true,
				ExpectError: regexp.MustCompile(`(?s)Unsupported postgres version change.*-refresh=false`),
			},
		},
	})
}
