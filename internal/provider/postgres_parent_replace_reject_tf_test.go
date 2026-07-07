package provider

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"
)

// skipUnlessTFAcc skips before any side effect: resource.Test also skips on unset TF_ACC,
// but only once reached, after the fake backend already bound a socket.
func skipUnlessTFAcc(t *testing.T) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC not set; this drives real terraform against a network-boundary fake")
	}
}

// The wire-fake echoes back the requested name: setPostgresStateResource overwrites
// state.name from the response, so a fixed name would drift from config.
func pgNameFromPath(p string) string {
	i := strings.LastIndex(p, "/postgres/")
	if i < 0 {
		return ""
	}
	rest := p[i+len("/postgres/"):]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// A network-boundary fake serving just enough for a primary to reach running, refresh, and
// be destroyed; the parent-set replace step is plan-only, so no child create is ever served.
func pgReplaceFakeBackend() *httptest.Server {
	var deleted atomic.Bool
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/config"):
			writePGJSON(w, http.StatusOK, ubicloud_client.PostgresConfig{PgConfig: map[string]string{}, PgbouncerConfig: map[string]string{}})
		case r.Method == http.MethodPost:
			body := pgBodyForState(postgresStateRunning)
			body.Name = pgNameFromPath(r.URL.Path)
			writePGJSON(w, http.StatusOK, body)
		default: // the readiness/refresh detail GET
			if deleted.Load() {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
				return
			}
			body := pgBodyForState(postgresStateRunning)
			body.Name = pgNameFromPath(r.URL.Path)
			writePGJSON(w, http.StatusOK, body)
		}
	}))
}

// The settable inputs mirror the fake's fixed snapshot so step-1 apply is plan-consistent;
// private_subnet_name is omitted because it ConflictsWith(parent) and would block the replace step.
func pgPrimaryConfig(name string) string {
	return providerConfig + `
resource "ubicloud_postgres" "cb" {
  project_id       = "pjaaaaaaaaaaaaaaaaaaaaaaaa"
  location         = "aws-us-east-1"
  name             = "` + name + `"
  size             = "m8gd.large"
  storage_size     = 118
  version          = "17"
  ha_type          = "none"
  tags             = [{ key = "team", value = "data" }]
  pg_config        = {}
  pgbouncer_config = {}
}`
}

// The create-branch guard re-plans with a null prior and ignores unknowns, so an interpolated
// input would destroy the primary (destroy-before-create) for nothing; reject at plan instead.
func TestAccPostgresInheritedWithParentRejectedAtPlanOnReplace(t *testing.T) {
	cases := []struct {
		name      string
		extra     string
		inherited string
		wantErr   *regexp.Regexp
	}{
		{
			name:      "explicit size",
			inherited: `size = "m8gd.xlarge"`,
			wantErr:   regexp.MustCompile(`size cannot be set when parent is specified`),
		},
		{
			name:      "explicit version",
			inherited: `version = "16"`,
			wantErr:   regexp.MustCompile(`version cannot be set when parent is specified`),
		},
		{
			name:      "interpolated size",
			extra:     "resource \"terraform_data\" \"sz\" {\n  input = \"m8gd.xlarge\"\n}\n",
			inherited: `size = terraform_data.sz.output`,
			wantErr:   regexp.MustCompile(`size cannot be set when parent is specified`),
		},
		{
			name:      "interpolated version",
			extra:     "resource \"terraform_data\" \"v\" {\n  input = \"16\"\n}\n",
			inherited: `version = terraform_data.v.output`,
			wantErr:   regexp.MustCompile(`version cannot be set when parent is specified`),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			skipUnlessTFAcc(t)
			withFastPoll(t)
			srv := pgReplaceFakeBackend()
			defer srv.Close()
			t.Setenv("UBICLOUD_API_ENDPOINT", srv.URL)
			t.Setenv("UBICLOUD_API_TOKEN", "pat-test")

			resName := GetRandomResourceName("pgcb")
			replaceCfg := providerConfig + "\n" + c.extra + `
resource "ubicloud_postgres" "cb" {
  project_id = "pjaaaaaaaaaaaaaaaaaaaaaaaa"
  location   = "aws-us-east-1"
  name       = "` + resName + `"
  parent     = "tf-acc-src"
  ` + c.inherited + `
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
						Config:      replaceCfg,
						PlanOnly:    true,
						ExpectError: c.wantErr,
					},
				},
			})
		})
	}
}
