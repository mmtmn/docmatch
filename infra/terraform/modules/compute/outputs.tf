output "app_enabled" {
  description = "Whether the launch templates and Auto Scaling groups exist (an AMI id was provided)."
  value       = local.app_enabled
}

output "app_autoscaling_group_name" {
  description = "Name of the application Auto Scaling group (empty before the AMI exists)."
  value       = local.app_enabled ? aws_autoscaling_group.app[0].name : ""
}

output "worker_autoscaling_group_name" {
  description = "Name of the burst (spot) worker Auto Scaling group (empty before the AMI exists)."
  value       = local.app_enabled ? aws_autoscaling_group.worker[0].name : ""
}

output "worker_asg_name" {
  description = "Fixed name of the burst worker Auto Scaling group (known before it exists; DM_BURST_ASG_NAME)."
  value       = local.worker_asg_name
}

output "burst_api_url_parameter" {
  description = "SSM parameter the application writes its internal endpoint to at boot (DM_BURST_API_URL)."
  value       = aws_ssm_parameter.burst_api_url.name
}

output "app_role_arn" {
  description = "ARN of the application instance role."
  value       = aws_iam_role.app.arn
}

output "worker_role_arn" {
  description = "ARN of the worker instance role."
  value       = aws_iam_role.worker.arn
}

output "packer_instance_profile_name" {
  description = "Instance profile for the Packer build instance (empty unless create_packer_profile)."
  value       = var.create_packer_profile ? aws_iam_instance_profile.packer[0].name : ""
}

output "elastic_ip" {
  description = "Public IPv4 of the application instance (the CloudFront origin)."
  value       = aws_eip.app.public_ip
}

output "origin_fqdn" {
  description = "Origin hostname pointing at the Elastic IP."
  value       = aws_route53_record.origin.fqdn
}

output "data_volume_id" {
  description = "Id of the persistent data volume."
  value       = aws_ebs_volume.data.id
}
