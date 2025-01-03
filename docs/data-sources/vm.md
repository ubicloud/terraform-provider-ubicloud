---
# generated by https://github.com/hashicorp/terraform-plugin-docs
page_title: "ubicloud_vm Data Source - ubicloud"
subcategory: ""
description: |-
  Get information about a Ubicloud virtual machine.
---

# ubicloud_vm (Data Source)

Get information about a Ubicloud virtual machine.

## Example Usage

```terraform
variable "project_id" {
  description = "Ubicloud project"
  type        = string
  default     = "pj01qy4sty1j7nycv8hfqmgy6t"
}

variable "location" {
  description = "Ubicloud location"
  type        = string
  default     = "eu-central-h1"
}

data "ubicloud_vm" "example" {
  project_id = var.project_id
  location   = var.location
  name       = "vm-example"
}

output "example_vm" {
  value = data.ubicloud_vm.example
}
```

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `location` (String) The Ubicloud location/region
- `name` (String) Virtual machine name
- `project_id` (String) ID of the project

### Read-Only

- `firewalls` (Attributes List) List of firewalls (see [below for nested schema](#nestedatt--firewalls))
- `id` (String) ID of the VM
- `ip4` (String) IPv4 address
- `ip6` (String) IPv6 address
- `private_ipv4` (String) Private IPv4 address
- `private_ipv6` (String) Private IPv6 address
- `size` (String) Size of the underlying VM
- `state` (String) State of the VM
- `storage_size_gib` (Number) Storage size in GiB
- `subnet` (String) Subnet of the VM
- `unix_user` (String) Unix user of the VM

<a id="nestedatt--firewalls"></a>
### Nested Schema for `firewalls`

Read-Only:

- `description` (String) Description of the firewall
- `firewall_rules` (Attributes List) List of firewall rules (see [below for nested schema](#nestedatt--firewalls--firewall_rules))
- `id` (String) ID of the firewall
- `location` (String) Location of the firewall
- `name` (String) Name of the firewall

<a id="nestedatt--firewalls--firewall_rules"></a>
### Nested Schema for `firewalls.firewall_rules`

Read-Only:

- `cidr` (String) CIDR of the firewall rule
- `id` (String) ID of the firewall rule
- `port_range` (String) Port range of the firewall rule
