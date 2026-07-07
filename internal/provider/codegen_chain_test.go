package provider

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// These run the actual go:generate jq filters under config/, so a broken guard or a
// non-idempotent append turns red here; skip when jq is absent so a jq-less `go test` passes.
func runJQ(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	if _, lookErr := exec.LookPath("jq"); lookErr != nil {
		t.Skip("jq not installed; the codegen chain requires it")
	}
	cmd := exec.Command("jq", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errbuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errbuf
	err = cmd.Run()
	return out.String(), errbuf.String(), err
}

func jsonDeepEqual(t *testing.T, a, b string) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal([]byte(a), &av); err != nil {
		t.Fatalf("unmarshal first: %v", err)
	}
	if err := json.Unmarshal([]byte(b), &bv); err != nil {
		t.Fatalf("unmarshal second: %v", err)
	}
	return reflect.DeepEqual(av, bv)
}

func postgresAttrNames(t *testing.T, spec string) []string {
	t.Helper()
	var doc struct {
		Resources []struct {
			Name   string `json:"name"`
			Schema struct {
				Attributes []struct {
					Name string `json:"name"`
				} `json:"attributes"`
			} `json:"schema"`
		} `json:"resources"`
	}
	if err := json.Unmarshal([]byte(spec), &doc); err != nil {
		t.Fatalf("unmarshal spec: %v", err)
	}
	for _, r := range doc.Resources {
		if r.Name == "postgres" {
			names := make([]string, 0, len(r.Schema.Attributes))
			for _, a := range r.Schema.Attributes {
				names = append(names, a.Name)
			}
			return names
		}
	}
	return nil
}

// A select(.name == "postgres") that matches nothing makes an unguarded filter emit EMPTY
// output at exit 0, silently wiping every other resource; the guard must make it an identity.
func TestInjectRestoreTargetPassesThroughWithoutPostgres(t *testing.T) {
	in := `{"resources":[{"name":"vm","schema":{"attributes":[{"name":"id","string":{}}]}}],"datasources":[]}`
	out, stderr, err := runJQ(t, in, "-f", "../../config/inject_restore_target.jq")
	if err != nil {
		t.Fatalf("jq failed: %v; stderr: %s", err, stderr)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("inject_restore_target.jq wiped a no-postgres spec to empty output (unguarded select)")
	}
	if !jsonDeepEqual(t, in, out) {
		t.Errorf("no-postgres spec must pass through unchanged;\n in:  %s\n out: %s", in, out)
	}
}

// The pass-through guard must not disable the real injection.
func TestInjectRestoreTargetAddsAttributeForPostgres(t *testing.T) {
	in := `{"resources":[{"name":"postgres","schema":{"attributes":[{"name":"size","string":{}}]}}],"datasources":[]}`
	out, stderr, err := runJQ(t, in, "-f", "../../config/inject_restore_target.jq")
	if err != nil {
		t.Fatalf("jq failed: %v; stderr: %s", err, stderr)
	}
	names := postgresAttrNames(t, out)
	found := false
	for _, n := range names {
		if n == "restore_target" {
			found = true
		}
	}
	if !found {
		t.Errorf("restore_target not injected; postgres attributes: %v", names)
	}
}

// The final go:generate step must exit non-zero on a wiped or regressed spec (failing
// `go generate` loudly) and 0 on the real generated spec.
func TestChainAssertionCatchesWipe(t *testing.T) {
	assertJQ := "../../config/assert_postgres_resource.jq"
	if _, _, err := runJQ(t, `{"resources":[],"datasources":[]}`, "-e", "-f", assertJQ); err == nil {
		t.Error("assertion passed on a no-postgres spec; a chain wipe would go undetected")
	}
	if _, _, err := runJQ(t, `{"resources":[{"name":"postgres","schema":{"attributes":[{"name":"a"}]}}]}`, "-e", "-f", assertJQ); err == nil {
		t.Error("assertion passed on a postgres resource with the wrong attribute count")
	}
	spec := "../../config/generated/provider_code_spec_mod.json"
	if _, statErr := os.Stat(spec); statErr != nil {
		t.Skipf("generated spec not present (run go generate first): %v", statErr)
	}
	if _, stderr, err := runJQ(t, "", "-e", "-f", assertJQ, spec); err != nil {
		t.Errorf("assertion failed on the real generated spec: %v; stderr: %s", err, stderr)
	}
	// The timeouts block lives under .schema.blocks, so the attribute-count tripwire cannot see its loss.
	noTimeouts, stderr, err := runJQ(t, "", `del(.resources[] | select(.name == "postgres") | .schema.blocks[] | select(.name == "timeouts"))`, spec)
	if err != nil {
		t.Fatalf("jq del(timeouts) failed: %v; stderr: %s", err, stderr)
	}
	if _, _, err := runJQ(t, noTimeouts, "-e", "-f", assertJQ); err == nil {
		t.Error("assertion passed after the timeouts block was dropped; an inject_timeouts.jq regression would go undetected")
	}
	// A rename keeps the attribute count, so only the by-name check can catch it.
	renamed, stderr, err := runJQ(t, "", `(.resources[] | select(.name == "postgres") | .schema.attributes[] | select(.name == "restore_target") | .name) = "renamed"`, spec)
	if err != nil {
		t.Fatalf("jq rename(restore_target) failed: %v; stderr: %s", err, stderr)
	}
	if _, _, err := runJQ(t, renamed, "-e", "-f", assertJQ); err == nil {
		t.Error("assertion passed after restore_target was renamed with the count unchanged; an inject_restore_target.jq regression would go undetected")
	}
}

// Re-applying a filter to the generated spec (already one application in) must no-op: a
// non-idempotent += duplicates validator rows or appended sentences into schema and docs.
func TestJqPatchFilesAreIdempotent(t *testing.T) {
	spec := "../../config/generated/provider_code_spec_mod.json"
	data, err := os.ReadFile(spec)
	if err != nil {
		t.Skipf("generated spec not present (run go generate first): %v", err)
	}
	for _, f := range []string{"classify.jq", "inject_restore_target.jq", "plan_modifiers.jq", "inject_timeouts.jq"} {
		t.Run(f, func(t *testing.T) {
			out, stderr, err := runJQ(t, string(data), "-f", "../../config/"+f)
			if err != nil {
				t.Fatalf("jq -f %s failed: %v; stderr: %s", f, err, stderr)
			}
			if !jsonDeepEqual(t, string(data), out) {
				t.Errorf("%s is not idempotent: re-applying it to the generated spec changed the spec", f)
			}
		})
	}
}
