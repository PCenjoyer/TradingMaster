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
