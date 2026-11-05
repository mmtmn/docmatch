# Compute: one on-demand t4g.small in an Auto Scaling group of exactly one (self-healing, rolling
# AMI refresh), a persistent gp3 data volume re-attached by user-data, an Elastic IP that is the
# CloudFront origin, and a stage-1 spot group for workers that starts at zero.

locals {
  app_enabled      = var.ami_id != ""
  data_mount_point = "/var/lib/postgresql"
  common_tags      = merge(var.tags, { "docmatch:env" = var.env })

  user_data_app = templatefile("${path.module}/templates/user-data.sh.tftpl", {
    role              = "app"
    name              = var.name
    env               = var.env
    region            = var.region
    ssm_path_prefix   = var.ssm_path_prefix
    data_volume_id    = aws_ebs_volume.data.id
    eip_allocation_id = aws_eip.app.allocation_id
    data_mount_point  = local.data_mount_point
  })

  user_data_worker = templatefile("${path.module}/templates/user-data.sh.tftpl", {
    role              = "worker"
    name              = var.name
    env               = var.env
    region            = var.region
    ssm_path_prefix   = var.ssm_path_prefix
    data_volume_id    = ""
    eip_allocation_id = ""
    data_mount_point  = local.data_mount_point
  })
}

# --------------------------------------------------------------------------- persistent data
resource "aws_ebs_volume" "data" {
  availability_zone = var.availability_zone
  size              = var.data_volume_size
  type              = "gp3"
  iops              = 3000
  throughput        = 125
  encrypted         = true
  final_snapshot    = true

  tags = merge(local.common_tags, {
    Name                = "${var.name}-data"
    "docmatch:role"     = "app"
    "docmatch:snapshot" = "${var.name}-data"
  })
}

resource "aws_eip" "app" {
  domain = "vpc"
  tags   = merge(local.common_tags, { Name = "${var.name}-app", "docmatch:role" = "app" })
}

# CloudFront reaches the instance through this name; Caddy holds a certificate for it.
resource "aws_route53_record" "origin" {
  zone_id = var.hosted_zone_id
  name    = var.origin_hostname
  type    = "A"
  ttl     = 300
  records = [aws_eip.app.public_ip]
}

# ------------------------------------------------------------------------------ application
resource "aws_launch_template" "app" {
  count = local.app_enabled ? 1 : 0

  name                   = "${var.name}-app"
  description            = "docmatch ${var.env} application instance"
  image_id               = var.ami_id
  instance_type          = var.instance_type
  ebs_optimized          = true
  update_default_version = true
  user_data              = base64encode(local.user_data_app)

  instance_initiated_shutdown_behavior = "stop"

  iam_instance_profile {
    arn = aws_iam_instance_profile.app.arn
  }

  network_interfaces {
    device_index                = 0
    associate_public_ip_address = true
    ipv6_address_count          = 1
    security_groups             = [var.security_group_id]
    delete_on_termination       = true
  }

  block_device_mappings {
    device_name = "/dev/sda1"
    ebs {
      volume_type           = "gp3"
      volume_size           = var.root_volume_size
      encrypted             = true
      delete_on_termination = true
    }
  }

  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
    http_protocol_ipv6          = "disabled"
    instance_metadata_tags      = "enabled"
  }

  monitoring {
    enabled = false
  }

  dynamic "tag_specifications" {
    for_each = ["instance", "volume", "network-interface"]
    content {
      resource_type = tag_specifications.value
      tags          = merge(local.common_tags, { Name = "${var.name}-app", "docmatch:role" = "app" })
    }
  }

  tags = merge(local.common_tags, { Name = "${var.name}-app" })
}

resource "aws_autoscaling_group" "app" {
  count = local.app_enabled ? 1 : 0

  name                      = "${var.name}-app"
  min_size                  = 1
  max_size                  = 1
  desired_capacity          = 1
  vpc_zone_identifier       = [var.subnet_id]
  health_check_type         = "EC2"
  health_check_grace_period = 300
  default_instance_warmup   = 180
  wait_for_capacity_timeout = "0"
  termination_policies      = ["OldestLaunchTemplate", "OldestInstance"]

  launch_template {
    id      = aws_launch_template.app[0].id
    version = aws_launch_template.app[0].latest_version
  }

  # One instance only: the old one is terminated before the new one starts (short downtime,
  # documented in infra/README.md), because the data volume can be attached to one instance.
  instance_refresh {
    strategy = "Rolling"
    preferences {
      min_healthy_percentage = 0
      max_healthy_percentage = 100
      instance_warmup        = 180
      skip_matching          = true
    }
  }

  enabled_metrics     = ["GroupInServiceInstances", "GroupDesiredCapacity", "GroupTerminatingInstances"]
  metrics_granularity = "1Minute"

  dynamic "tag" {
    for_each = merge(local.common_tags, { Name = "${var.name}-app", "docmatch:role" = "app" })
    content {
      key                 = tag.key
      value               = tag.value
      propagate_at_launch = true
    }
  }

  lifecycle {
    ignore_changes = [desired_capacity]
  }
}

# ---------------------------------------------------------------------------------- workers
resource "aws_launch_template" "worker" {
  count = local.app_enabled ? 1 : 0

  name                   = "${var.name}-worker"
  description            = "docmatch ${var.env} spot worker"
  image_id               = var.ami_id
  ebs_optimized          = true
  update_default_version = true
  user_data              = base64encode(local.user_data_worker)

  iam_instance_profile {
    arn = aws_iam_instance_profile.worker.arn
  }

  network_interfaces {
    device_index                = 0
    associate_public_ip_address = true
    ipv6_address_count          = 1
    security_groups             = [var.security_group_id]
    delete_on_termination       = true
  }

  block_device_mappings {
    device_name = "/dev/sda1"
    ebs {
      volume_type           = "gp3"
      volume_size           = var.root_volume_size
      encrypted             = true
      delete_on_termination = true
    }
  }

  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
    http_protocol_ipv6          = "disabled"
    instance_metadata_tags      = "enabled"
  }

  monitoring {
    enabled = false
  }

  dynamic "tag_specifications" {
    for_each = ["instance", "volume", "network-interface"]
    content {
      resource_type = tag_specifications.value
      tags          = merge(local.common_tags, { Name = "${var.name}-worker", "docmatch:role" = "worker" })
    }
  }

  tags = merge(local.common_tags, { Name = "${var.name}-worker" })
}

resource "aws_autoscaling_group" "worker" {
  count = local.app_enabled ? 1 : 0

  name                      = "${var.name}-worker"
  min_size                  = 0
  max_size                  = var.worker_max_size
  desired_capacity          = var.worker_desired_capacity
  vpc_zone_identifier       = [var.subnet_id]
  health_check_type         = "EC2"
  health_check_grace_period = 180
  capacity_rebalance        = true
  wait_for_capacity_timeout = "0"

  mixed_instances_policy {
    instances_distribution {
      on_demand_base_capacity                  = 0
      on_demand_percentage_above_base_capacity = 0
      spot_allocation_strategy                 = "price-capacity-optimized"
    }

    launch_template {
      launch_template_specification {
        launch_template_id = aws_launch_template.worker[0].id
        version            = aws_launch_template.worker[0].latest_version
      }

      dynamic "override" {
        for_each = var.worker_instance_types
        content {
          instance_type = override.value
        }
      }
    }
  }

  instance_refresh {
    strategy = "Rolling"
    preferences {
      min_healthy_percentage = 0
      skip_matching          = true
    }
  }

  dynamic "tag" {
    for_each = merge(local.common_tags, { Name = "${var.name}-worker", "docmatch:role" = "worker" })
    content {
      key                 = tag.key
      value               = tag.value
      propagate_at_launch = true
    }
  }
}

# --------------------------------------------------------------------------- EBS snapshots
data "aws_iam_policy_document" "assume_dlm" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["dlm.amazonaws.com"]
    }
  }
}

data "aws_iam_policy_document" "dlm" {
  statement {
    sid    = "ManageSnapshots"
    effect = "Allow"
    actions = [
      "ec2:CreateSnapshot",
      "ec2:CreateSnapshots",
      "ec2:DeleteSnapshot",
      "ec2:DescribeInstances",
      "ec2:DescribeVolumes",
      "ec2:DescribeSnapshots",
    ]
    resources = ["*"]
  }

  statement {
    sid       = "TagSnapshots"
    effect    = "Allow"
    actions   = ["ec2:CreateTags"]
    resources = ["arn:aws:ec2:*::snapshot/*"]
  }
}

resource "aws_iam_role" "dlm" {
  count = var.enable_ebs_snapshots ? 1 : 0

  name               = "${var.name}-dlm"
  path               = "/docmatch/"
  assume_role_policy = data.aws_iam_policy_document.assume_dlm.json
  tags               = merge(local.common_tags, { Name = "${var.name}-dlm" })
}

resource "aws_iam_role_policy" "dlm" {
  count = var.enable_ebs_snapshots ? 1 : 0

  name   = "snapshots"
  role   = aws_iam_role.dlm[0].id
  policy = data.aws_iam_policy_document.dlm.json
}

resource "aws_dlm_lifecycle_policy" "data" {
  count = var.enable_ebs_snapshots ? 1 : 0

  description        = "docmatch ${var.env} daily snapshots of the data volume"
  execution_role_arn = aws_iam_role.dlm[0].arn
  state              = "ENABLED"

  policy_details {
    resource_types = ["VOLUME"]
    target_tags    = { "docmatch:snapshot" = "${var.name}-data" }

    schedule {
      name      = "daily"
      copy_tags = true

      create_rule {
        interval      = 24
        interval_unit = "HOURS"
        times         = [var.snapshot_time_utc]
      }

      retain_rule {
        count = var.snapshot_retention_count
      }

      tags_to_add = { "docmatch:snapshot-source" = "dlm" }
    }
  }

  tags = merge(local.common_tags, { Name = "${var.name}-data-snapshots" })
}
