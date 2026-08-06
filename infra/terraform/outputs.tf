output "cluster_name" {
  description = "Имя кластера EKS"
  value       = module.eks.cluster_name
}

output "cluster_endpoint" {
  description = "Endpoint Kubernetes API"
  value       = module.eks.cluster_endpoint
}

output "ecr_repository_url" {
  description = "Адрес репозитория контейнеров ECR"
  value       = aws_ecr_repository.tradingmaster.repository_url
}

output "configure_kubectl" {
  description = "Команда настройки kubectl"
  value       = format("aws eks update-kubeconfig --region %s --name %s", var.aws_region, module.eks.cluster_name)
}

output "postgres_endpoint" {
  description = "Endpoint общего PostgreSQL"
  value       = aws_db_instance.tradingmaster.address
}

output "postgres_master_secret_arn" {
  description = "ARN секрета с master credentials, управляемого RDS"
  value       = aws_db_instance.tradingmaster.master_user_secret[0].secret_arn
}

output "postgres_database_url_template" {
  description = "Шаблон TM_DATABASE_URL; PASSWORD получите из Secrets Manager"
  value       = format("postgres://tradingmaster_admin:<PASSWORD>@%s:5432/tradingmaster?sslmode=require", aws_db_instance.tradingmaster.address)
}
