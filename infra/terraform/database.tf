resource "aws_db_subnet_group" "tradingmaster" {
  name       = local.name
  subnet_ids = module.vpc.private_subnets

  tags = {
    Name = "${local.name}-postgres"
  }
}

resource "aws_security_group" "postgres" {
  name        = "${local.name}-postgres"
  description = "PostgreSQL access from TradingMaster EKS nodes"
  vpc_id      = module.vpc.vpc_id

  tags = {
    Name = "${local.name}-postgres"
  }
}

resource "aws_vpc_security_group_ingress_rule" "postgres_from_eks" {
  security_group_id            = aws_security_group.postgres.id
  referenced_security_group_id = module.eks.node_security_group_id
  from_port                    = 5432
  to_port                      = 5432
  ip_protocol                  = "tcp"
  description                  = "PostgreSQL from EKS worker nodes"
}

resource "aws_db_instance" "tradingmaster" {
  identifier = local.name

  engine         = "postgres"
  engine_version = var.postgres_engine_version
  instance_class = var.postgres_instance_class

  db_name  = "tradingmaster"
  username = "tradingmaster_admin"
  port     = 5432

  manage_master_user_password = true

  allocated_storage     = var.postgres_allocated_storage
  max_allocated_storage = var.postgres_max_allocated_storage
  storage_type          = "gp3"
  storage_encrypted     = true

  db_subnet_group_name   = aws_db_subnet_group.tradingmaster.name
  vpc_security_group_ids = [aws_security_group.postgres.id]
  publicly_accessible    = false
  multi_az               = var.postgres_multi_az

  backup_retention_period    = var.postgres_backup_retention_days
  auto_minor_version_upgrade = true
  copy_tags_to_snapshot      = true
  deletion_protection        = var.postgres_deletion_protection
  skip_final_snapshot        = var.postgres_skip_final_snapshot
  final_snapshot_identifier  = var.postgres_skip_final_snapshot ? null : "${local.name}-final"

  apply_immediately = false
}
