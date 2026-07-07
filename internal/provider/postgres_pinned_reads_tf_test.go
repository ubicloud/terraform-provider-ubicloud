package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"
)

// pgPinnedReadsFakeBackend serves a running primary carrying the read-only computeds this issue
// pins: a password, a firewall rule, and non-null restore times. It echoes tags from the PATCH so
// a tags update converges. When driftLatest is set, latest_restore_time advances on every read,
// modeling the ever-moving restore window so the apply read-back differs from the plan pin. A
// DELETE marks the row gone so the -refresh=false teardown converges.
func pgPinnedReadsFakeBackend(driftLatest bool) *httptest.Server {
	var deleted atomic.Bool
	var gets atomic.Int64
	var tags atomic.Pointer[[]ubicloud_client.PostgresTag]
	initial := []ubicloud_client.PostgresTag{{Key: "team", Value: "data"}}
	tags.Store(&initial)
	body := func(name string) ubicloud_client.PostgresDatabase {
		b := pgBodyForState(postgresStateRunning)
		b.Name = name
		b.Password = ptrTo("s3cret")
		b.EarliestRestoreTime = ptrTo("2026-07-07T00:00:00Z")
		b.LatestRestoreTime = "2026-07-07T01:00:00Z"
		if driftLatest {
			b.LatestRestoreTime = fmt.Sprintf("2026-07-07T00:00:00.%06dZ", gets.Add(1))
		}
		b.Tags = *tags.Load()
		b.FirewallRules = []ubicloud_client.PostgresFirewallRule{
			{Id: "fr0000000000000000000000aa", Cidr: "0.0.0.0/0", Description: ptrTo("default"), Port: ptrTo(5432)},
		}
		return b
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/config"):
			writePGJSON(w, http.StatusOK, ubicloud_client.PostgresConfig{PgConfig: map[string]string{}, PgbouncerConfig: map[string]string{}})
		case r.Method == http.MethodPatch:
			var pb struct {
				Tags []ubicloud_client.PostgresTag `json:"tags"`
			}
			_ = json.NewDecoder(r.Body).Decode(&pb)
			if pb.Tags != nil {
				tags.Store(&pb.Tags)
			}
			writePGJSON(w, http.StatusOK, body(pgNameFromPath(r.URL.Path)))
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

func pgPinnedReadsConfig(name, tags string) string {
	return providerConfig + `
resource "ubicloud_postgres" "pg" {
  project_id       = "pjaaaaaaaaaaaaaaaaaaaaaaaa"
  location         = "aws-us-east-1"
  name             = "` + name + `"
  size             = "m8gd.large"
  storage_size     = 118
  version          = "17"
  ha_type          = "none"
  tags             = ` + tags + `
  pg_config        = {}
  pgbouncer_config = {}
}`
}

const (
	pgPinnedReadsOneTag = `[{ key = "team", value = "data" }]`
	pgPinnedReadsTwoTag = `[{ key = "team", value = "data" }, { key = "env", value = "prod" }]`
)

// TestAccPostgresUpdatePlanPinsReadOnlyComputeds is the rendering probe: on a tags-only in-place
// update, the pinned read-only computeds render KNOWN (no "known after apply" churn) while the
// deliberately unpinned hostname/connection_string render UNKNOWN because they rotate on an
// upgrade. Latest stays fixed here so the check isolates plan rendering from the apply-tail hold.
func TestAccPostgresUpdatePlanPinsReadOnlyComputeds(t *testing.T) {
	skipUnlessTFAcc(t)
	withFastPoll(t)
	srv := pgPinnedReadsFakeBackend(false)
	defer srv.Close()
	t.Setenv("UBICLOUD_API_ENDPOINT", srv.URL)
	t.Setenv("UBICLOUD_API_TOKEN", "pat-test")

	name := GetRandomResourceName("pgpin")
	addr := "ubicloud_postgres.pg"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: pgPinnedReadsConfig(name, pgPinnedReadsOneTag)},
			{
				Config: pgPinnedReadsConfig(name, pgPinnedReadsTwoTag),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectKnownValue(addr, tfjsonpath.New("firewall_rules"), knownvalue.NotNull()),
						plancheck.ExpectKnownValue(addr, tfjsonpath.New("password"), knownvalue.NotNull()),
						plancheck.ExpectKnownValue(addr, tfjsonpath.New("earliest_restore_time"), knownvalue.NotNull()),
						plancheck.ExpectKnownValue(addr, tfjsonpath.New("latest_restore_time"), knownvalue.NotNull()),
						plancheck.ExpectUnknownValue(addr, tfjsonpath.New("hostname")),
						plancheck.ExpectUnknownValue(addr, tfjsonpath.New("connection_string")),
					},
				},
			},
		},
	})
}

// TestAccPostgresPinnedReadDriftNoInconsistentResult is the inconsistent-result regression: the
// fake advances latest_restore_time on every read, so the apply read-back always differs from the
// value the plan pinned. Without the Update-tail hold, terraform raises "Provider produced
// inconsistent result after apply"; the tags-only step applying cleanly is the guard.
func TestAccPostgresPinnedReadDriftNoInconsistentResult(t *testing.T) {
	skipUnlessTFAcc(t)
	withFastPoll(t)
	srv := pgPinnedReadsFakeBackend(true)
	defer srv.Close()
	t.Setenv("UBICLOUD_API_ENDPOINT", srv.URL)
	t.Setenv("UBICLOUD_API_TOKEN", "pat-test")

	name := GetRandomResourceName("pgdrift")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: pgPinnedReadsConfig(name, pgPinnedReadsOneTag)},
			{Config: pgPinnedReadsConfig(name, pgPinnedReadsTwoTag)},
		},
	})
}
