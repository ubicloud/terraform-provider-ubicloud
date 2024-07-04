data "ubicloud_project" "example" {
  id = "pjqp2p1bgxqe2n3wxe62dtsby6"
}
output "example_project" {
  value = data.ubicloud_project.example
}
