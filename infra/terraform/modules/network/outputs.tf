output "vpc_id" {
  description = "Id of the VPC."
  value       = aws_vpc.this.id
}

output "vpc_cidr" {
  description = "IPv4 CIDR of the VPC."
  value       = aws_vpc.this.cidr_block
}

output "vpc_ipv6_cidr" {
  description = "Amazon-provided IPv6 CIDR of the VPC."
  value       = aws_vpc.this.ipv6_cidr_block
}

output "availability_zone" {
  description = "Availability zone used by both subnets (the EBS data volume must live here too)."
  value       = local.az
}

output "app_subnet_id" {
  description = "Public subnet for the application instance and the Packer build instance."
  value       = aws_subnet.app.id
}

output "sandbox_subnet_id" {
  description = "Isolated subnet for the sandbox Lambda functions."
  value       = aws_subnet.sandbox.id
}

output "app_security_group_id" {
  description = "Security group of the application instance."
  value       = aws_security_group.app.id
}

output "burst_security_group_id" {
  description = "Security group of the burst workers (no ingress; reaches the app's internal endpoint)."
  value       = aws_security_group.burst.id
}

output "app_subnet_cidr" {
  description = "IPv4 CIDR of the application subnet (the burst workers live there too)."
  value       = aws_subnet.app.cidr_block
}

output "sandbox_security_group_id" {
  description = "Security group of the sandbox Lambda functions."
  value       = aws_security_group.sandbox.id
}

output "cloudfront_prefix_list_id" {
  description = "Id of the managed CloudFront origin-facing prefix list in this region."
  value       = data.aws_ec2_managed_prefix_list.cloudfront.id
}

output "s3_endpoint_id" {
  description = "Id of the S3 gateway endpoint."
  value       = aws_vpc_endpoint.s3.id
}

output "s3_endpoint_prefix_list_id" {
  description = "Prefix list id of the S3 gateway endpoint (for security group rules)."
  value       = aws_vpc_endpoint.s3.prefix_list_id
}
