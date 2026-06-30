package provider

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/datasource_postgres"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"
)

func ptrTo[T any](v T) *T { return &v }

// sampleDetailedPostgresResponse mimics the detailed GET a primary returns at
// state=creating: empty (not null) hostname/connection-string, nil resize/upgrade
// targets, and absent earliest/latest restore time (no timeline yet).
func sampleDetailedPostgresResponse() ubicloud_client.PostgresDatabase {
	createdAt, _ := time.Parse(time.RFC3339, "2026-06-22T18:29:41+00:00")
	return ubicloud_client.PostgresDatabase{
		Id:                       "pgn30gjk1d1e2jj34v9x0dq4rp",
		Name:                     "tf-acc-pg-read",
		State:                    "creating",
		Location:                 "aws-us-east-1",
		VmSize:                   "m8gd.large",
		StorageSizeGib:           118,
		Primary:                  true,
		HaType:                   "none",
		Version:                  ubicloud_client.PostgresDatabaseVersionN17,
		Flavor:                   "standard",
		TargetVmSize:             nil,
		TargetStorageSizeGib:     nil,
		TargetVersion:            ubicloud_client.PostgresDatabaseTargetVersionN17,
		TargetServerCount:        1,
		MaintenanceWindowStartAt: nil,
		ReadReplica:              false,
		Parent:                   nil,
		FallbackActive:           false,
		CaCertificates:           ptrTo("-----BEGIN CERTIFICATE-----"),
		CreatedAt:                createdAt,
		Hostname:                 nil,
		Username:                 ptrTo("postgres"),
		Password:                 ptrTo("supersecret"),
		ConnectionString:         ptrTo("postgres://postgres:supersecret@:5432/postgres"),
		EarliestRestoreTime:      nil,
		LatestRestoreTime:        "",
		Tags:                     []ubicloud_client.PostgresTag{{Key: "team", Value: "data"}},
		FirewallRules: []ubicloud_client.PostgresFirewallRule{
			{Id: "pf0000000000000000000000fr", Cidr: "0.0.0.0/0", Description: ptrTo("default"), Port: ptrTo(5432)},
		},
	}
}

func TestSetPostgresStateResourceWiresReadSurface(t *testing.T) {
	ctx := t.Context()
	resp := sampleDetailedPostgresResponse()
	var m resource_postgres.PostgresModel
	if diags := setPostgresStateResource(ctx, &resp, &m); diags.HasError() {
		t.Fatalf("setPostgresStateResource diags: %+v", diags)
	}

	if got := m.Flavor.ValueString(); got != "standard" {
		t.Errorf("flavor = %q, want %q", got, "standard")
	}
	if got := m.TargetVersion.ValueString(); got != "17" {
		t.Errorf("target_version = %q, want %q", got, "17")
	}
	if got := m.TargetServerCount.ValueInt64(); got != 1 {
		t.Errorf("target_server_count = %d, want 1", got)
	}
	if got := m.CreatedAt.ValueString(); got != "2026-06-22T18:29:41+00:00" {
		t.Errorf("created_at = %q, want %q (offset preserved, not Z)", got, "2026-06-22T18:29:41+00:00")
	}
	if got := m.Username.ValueString(); got != "postgres" {
		t.Errorf("username = %q, want postgres", got)
	}
	if got := m.Password.ValueString(); got != "supersecret" {
		t.Errorf("password = %q, want supersecret", got)
	}
	if got := m.CaCertificates.ValueString(); got != "-----BEGIN CERTIFICATE-----" {
		t.Errorf("ca_certificates = %q", got)
	}

	// size and storage_size are INPUTS that must round-trip from the actual VmSize /
	// StorageSizeGib, the way an import repopulates them. An asymmetry (size mapped,
	// storage_size not) leaves storage_size null after import -> spurious plan diff ->
	// a hard 400 on a read replica. See pg-import-storage-size-roundtrip.
	if got := m.Size.ValueString(); got != "m8gd.large" {
		t.Errorf("size = %q, want m8gd.large (round-trips from VmSize)", got)
	}
	if m.StorageSize.IsNull() || m.StorageSize.ValueInt64() != 118 {
		t.Errorf("storage_size = %d (null=%v), want 118 (round-trips from StorageSizeGib)", m.StorageSize.ValueInt64(), m.StorageSize.IsNull())
	}

	// The four detailed read fields v0.3.0 dropped from the resource schema but kept
	// on the data source (see pg-resource-schema-dropped-reads). state is the epic's
	// convergence signal; it must read from the resource, not only the data source.
	if got := m.State.ValueString(); got != "creating" {
		t.Errorf("state = %q, want creating", got)
	}
	if got := m.ConnectionString.ValueString(); got != "postgres://postgres:supersecret@:5432/postgres" {
		t.Errorf("connection_string = %q", got)
	}
	// Absent on a primary before first backup / on replicas: nil-safe -> null.
	if !m.EarliestRestoreTime.IsNull() {
		t.Errorf("earliest_restore_time = %q, want null pre-backup", m.EarliestRestoreTime.ValueString())
	}
	// Non-nullable in the spec; empty (not null) at creating -> known empty string.
	if m.LatestRestoreTime.IsNull() {
		t.Error("latest_restore_time is null, want known empty string at creating")
	}
	if got := m.LatestRestoreTime.ValueString(); got != "" {
		t.Errorf("latest_restore_time = %q, want empty at creating", got)
	}

	// Zero-valued-but-present: must be KNOWN, not null, to distinguish unwired.
	if m.ReadReplica.IsNull() || m.ReadReplica.ValueBool() != false {
		t.Errorf("read_replica = %v (null=%v), want known false", m.ReadReplica.ValueBool(), m.ReadReplica.IsNull())
	}
	if m.FallbackActive.IsNull() || m.FallbackActive.ValueBool() != false {
		t.Errorf("fallback_active = %v (null=%v), want known false", m.FallbackActive.ValueBool(), m.FallbackActive.IsNull())
	}
	// Nullable: at creating no window is set and the serializer emits null. A null
	// must map to Int64Null, not 0 (0 is a real midnight window, indistinguishable
	// from unset). See pg-maintenance-window-nullable-mapping.
	if !m.MaintenanceWindowStartAt.IsNull() {
		t.Errorf("maintenance_window_start_at = %d (null=%v), want null at creating", m.MaintenanceWindowStartAt.ValueInt64(), m.MaintenanceWindowStartAt.IsNull())
	}
	// hostname is null (not empty) at creating: no DNS zone and the VM has no IP
	// yet, so the serializer emits a nil pointer (verified live, pg at creating).
	if !m.Hostname.IsNull() {
		t.Errorf("hostname = %q, want null at creating", m.Hostname.ValueString())
	}

	// Nil pointers map to null, not panic, not empty-string.
	if !m.Parent.IsNull() {
		t.Errorf("parent = %q, want null for non-replica", m.Parent.ValueString())
	}
	if !m.TargetVmSize.IsNull() {
		t.Errorf("target_vm_size = %q, want null (no resize target)", m.TargetVmSize.ValueString())
	}
	if !m.TargetStorageSizeGib.IsNull() {
		t.Errorf("target_storage_size_gib = %d, want null", m.TargetStorageSizeGib.ValueInt64())
	}

	if m.Tags.IsNull() || len(m.Tags.Elements()) != 1 {
		t.Fatalf("tags null=%v len=%d, want one element", m.Tags.IsNull(), len(m.Tags.Elements()))
	}
	// The shared GetPostgresTagsState helper emits datasource_postgres.TagsValue for
	// both paths (same as the firewall-rules helper); the nested object{key,value} is
	// structurally identical so the resource schema accepts it.
	if tag, ok := m.Tags.Elements()[0].(datasource_postgres.TagsValue); !ok {
		t.Errorf("tag elem type %T, want datasource_postgres.TagsValue", m.Tags.Elements()[0])
	} else if tag.Key.ValueString() != "team" || tag.Value.ValueString() != "data" {
		t.Errorf("tag = {%q:%q}, want {team:data}", tag.Key.ValueString(), tag.Value.ValueString())
	}
}

func TestSetPostgresStateDatasourceWiresReadSurface(t *testing.T) {
	ctx := t.Context()
	resp := sampleDetailedPostgresResponse()
	var m datasource_postgres.PostgresModel
	if diags := setPostgresStateDatasource(ctx, &resp, &m); diags.HasError() {
		t.Fatalf("setPostgresStateDatasource diags: %+v", diags)
	}

	if got := m.State.ValueString(); got != "creating" {
		t.Errorf("state = %q, want creating", got)
	}
	if got := m.Flavor.ValueString(); got != "standard" {
		t.Errorf("flavor = %q, want standard", got)
	}
	if got := m.TargetVersion.ValueString(); got != "17" {
		t.Errorf("target_version = %q, want 17", got)
	}
	if got := m.TargetServerCount.ValueInt64(); got != 1 {
		t.Errorf("target_server_count = %d, want 1", got)
	}
	if got := m.CreatedAt.ValueString(); got != "2026-06-22T18:29:41+00:00" {
		t.Errorf("created_at = %q", got)
	}
	if got := m.Username.ValueString(); got != "postgres" {
		t.Errorf("username = %q, want postgres", got)
	}
	if got := m.Password.ValueString(); got != "supersecret" {
		t.Errorf("password = %q, want supersecret", got)
	}
	if got := m.CaCertificates.ValueString(); got != "-----BEGIN CERTIFICATE-----" {
		t.Errorf("ca_certificates = %q", got)
	}
	if got := m.ConnectionString.ValueString(); got != "postgres://postgres:supersecret@:5432/postgres" {
		t.Errorf("connection_string = %q", got)
	}

	if m.ReadReplica.IsNull() || m.ReadReplica.ValueBool() != false {
		t.Errorf("read_replica null=%v, want known false", m.ReadReplica.IsNull())
	}
	if m.FallbackActive.IsNull() || m.FallbackActive.ValueBool() != false {
		t.Errorf("fallback_active null=%v, want known false", m.FallbackActive.IsNull())
	}
	if !m.Hostname.IsNull() {
		t.Errorf("hostname = %q, want null at creating", m.Hostname.ValueString())
	}
	// Nullable: null at creating maps to Int64Null, not 0. See
	// pg-maintenance-window-nullable-mapping.
	if !m.MaintenanceWindowStartAt.IsNull() {
		t.Errorf("maintenance_window_start_at = %d (null=%v), want null at creating", m.MaintenanceWindowStartAt.ValueInt64(), m.MaintenanceWindowStartAt.IsNull())
	}

	// Absent on a primary before first backup / on replicas: nil-safe.
	if !m.EarliestRestoreTime.IsNull() {
		t.Errorf("earliest_restore_time = %q, want null pre-backup", m.EarliestRestoreTime.ValueString())
	}
	if !m.Parent.IsNull() {
		t.Errorf("parent = %q, want null", m.Parent.ValueString())
	}

	if m.Tags.IsNull() || len(m.Tags.Elements()) != 1 {
		t.Fatalf("tags null=%v len=%d, want one element", m.Tags.IsNull(), len(m.Tags.Elements()))
	}
	if tag, ok := m.Tags.Elements()[0].(datasource_postgres.TagsValue); !ok {
		t.Errorf("tag elem type %T, want datasource_postgres.TagsValue", m.Tags.Elements()[0])
	} else if tag.Key.ValueString() != "team" || tag.Value.ValueString() != "data" {
		t.Errorf("tag = {%q:%q}, want {team:data}", tag.Key.ValueString(), tag.Value.ValueString())
	}
}

// The read paths now write password (both) and connection_string (data source)
// into state; the generated schema must mark them sensitive so the framework
// redacts them from plan/state output.
func TestPostgresPasswordFieldsAreSensitive(t *testing.T) {
	ctx := t.Context()

	rs := resource_postgres.PostgresResourceSchema(ctx)
	if !rs.Attributes["password"].IsSensitive() {
		t.Error("resource password must be Sensitive (carries the superuser password)")
	}
	// connection_string is restored onto the resource by pg-resource-schema-dropped-reads
	// and embeds the password, so it must mirror the data source's Sensitive flag.
	if !rs.Attributes["connection_string"].IsSensitive() {
		t.Error("resource connection_string must be Sensitive (embeds the password)")
	}

	ds := datasource_postgres.PostgresDataSourceSchema(ctx)
	if !ds.Attributes["password"].IsSensitive() {
		t.Error("data source password must be Sensitive")
	}
	if !ds.Attributes["connection_string"].IsSensitive() {
		t.Error("data source connection_string must be Sensitive (embeds the password)")
	}
}

// At creating-state no maintenance window is set and the serializer emits
// maintenance_window_start_at: null (verified in raw traffic). A null must map to
// Int64Null, not 0: 0 is a real midnight window and would be indistinguishable from
// "unset". Built by JSON-unmarshal so the wire null reaches the client type as the
// API delivers it (a struct literal cannot express null on the detailed value field).
func TestMaintenanceWindowStartAtNullMapsToNull(t *testing.T) {
	ctx := t.Context()
	body := `{"id":"pgn30gjk1d1e2jj34v9x0dq4rp","name":"tf-acc-pg-mw-null","state":"creating",` +
		`"location":"aws-us-east-1","vm_size":"m8gd.large","storage_size_gib":118,"primary":true,` +
		`"ha_type":"none","version":"17","flavor":"standard","target_version":"17","target_server_count":1,` +
		`"maintenance_window_start_at":null,"read_replica":false,"fallback_active":false,` +
		`"created_at":"2026-06-22T18:29:41+00:00","latest_restore_time":"","tags":[],"firewall_rules":[]}`
	var resp ubicloud_client.PostgresDatabase
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal detailed response: %v", err)
	}

	var mr resource_postgres.PostgresModel
	if diags := setPostgresStateResource(ctx, &resp, &mr); diags.HasError() {
		t.Fatalf("setPostgresStateResource diags: %+v", diags)
	}
	if !mr.MaintenanceWindowStartAt.IsNull() {
		t.Errorf("resource maintenance_window_start_at = %d (null=%v), want null for a null API value",
			mr.MaintenanceWindowStartAt.ValueInt64(), mr.MaintenanceWindowStartAt.IsNull())
	}

	var md datasource_postgres.PostgresModel
	if diags := setPostgresStateDatasource(ctx, &resp, &md); diags.HasError() {
		t.Fatalf("setPostgresStateDatasource diags: %+v", diags)
	}
	if !md.MaintenanceWindowStartAt.IsNull() {
		t.Errorf("datasource maintenance_window_start_at = %d (null=%v), want null for a null API value",
			md.MaintenanceWindowStartAt.ValueInt64(), md.MaintenanceWindowStartAt.IsNull())
	}
}

// A set maintenance window must round-trip as a known value (the non-null branch of
// the same nullable mapping), so the fix does not turn every window into null.
func TestMaintenanceWindowStartAtSetMapsToValue(t *testing.T) {
	ctx := t.Context()
	resp := sampleDetailedPostgresResponse()
	resp.MaintenanceWindowStartAt = ptrTo(15)

	var mr resource_postgres.PostgresModel
	if diags := setPostgresStateResource(ctx, &resp, &mr); diags.HasError() {
		t.Fatalf("setPostgresStateResource diags: %+v", diags)
	}
	if mr.MaintenanceWindowStartAt.IsNull() || mr.MaintenanceWindowStartAt.ValueInt64() != 15 {
		t.Errorf("resource maintenance_window_start_at null=%v val=%d, want known 15",
			mr.MaintenanceWindowStartAt.IsNull(), mr.MaintenanceWindowStartAt.ValueInt64())
	}

	var md datasource_postgres.PostgresModel
	if diags := setPostgresStateDatasource(ctx, &resp, &md); diags.HasError() {
		t.Fatalf("setPostgresStateDatasource diags: %+v", diags)
	}
	if md.MaintenanceWindowStartAt.IsNull() || md.MaintenanceWindowStartAt.ValueInt64() != 15 {
		t.Errorf("datasource maintenance_window_start_at null=%v val=%d, want known 15",
			md.MaintenanceWindowStartAt.IsNull(), md.MaintenanceWindowStartAt.ValueInt64())
	}
}
