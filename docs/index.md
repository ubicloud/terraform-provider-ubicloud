---
page_title: "Provider: Ubicloud"
---

# Ubicloud Provider

The "ubicloud" provider facilitates interaction with resources supported by [Ubicloud](https://www.ubicloud.com/). Before using this provider, you must configure it with your credentials, typically by setting the environment variable UBICLOUD_API_TOKEN. For instructions on obtaining an API token, refer to Ubicloud's [API documentation](https://www.ubicloud.com/docs/api/overview#Authentication).

For detailed information on the available resources, please refer to the links in the navigation bar.

## Example Usage

```terraform
terraform {
  required_providers {
    ubicloud = {
      source  = "ubicloud/ubicloud"
      version = "~> 0.1"
    }
  }
}

provider "ubicloud" {
  # Instead of setting api_token here, define a UBICLOUD_API_TOKEN
  # environment variable, e.g. by adding the following line to .bashrc:
  # export UBICLOUD_API_TOKEN="Your API TOKEN"
  api_token = "Your API TOKEN"
}

# Create a virtual machine
resource "ubicloud_vm" "web" {
  # ...
}
```

## Argument Reference

- `api_endpoint` (String) Ubicloud endpoint. If not set checks env for `UBICLOUD_API_ENDPOINT`. Default: `https://api.ubicloud.com`
- `api_token` (String, Sensitive) Ubicloud token. If not set checks env for `UBICLOUD_API_TOKEN`.
