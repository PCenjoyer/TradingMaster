variable "aws_region" {
  description = "Регион AWS"
  type        = string
  default     = "eu-central-1"
}

variable "environment" {
  description = "Имя окружения"
  type        = string
  default     = "dev"

  validation {
    condition     = can(regex("^[a-z0-9-]+$", var.environment))
    error_message = "Окружение может содержать только строчные латинские буквы, цифры и дефисы."
  }
}

variable "vpc_cidr" {
  description = "CIDR сети VPC"
  type        = string
  default     = "10.42.0.0/16"
}

variable "kubernetes_version" {
  description = "Версия Kubernetes в EKS"
  type        = string
  default     = "1.36"
}

variable "endpoint_public_access" {
  description = "Разрешить публичный endpoint Kubernetes API"
  type        = bool
  default     = false
}

variable "endpoint_public_access_cidrs" {
  description = "CIDR, которым разрешён доступ к публичному endpoint API"
  type        = list(string)
  default     = []

  validation {
    condition     = !var.endpoint_public_access || length(var.endpoint_public_access_cidrs) > 0
    error_message = "При публичном endpoint укажите хотя бы один доверенный CIDR."
  }
}

variable "single_nat_gateway" {
  description = "Один NAT Gateway для dev; для production используйте false"
  type        = bool
  default     = true
}

variable "node_instance_types" {
  description = "Допустимые типы EC2 для worker-узлов"
  type        = list(string)
  default     = ["t3.medium", "t3a.medium"]
}

variable "node_capacity_type" {
  description = "Тип ёмкости узлов: SPOT или ON_DEMAND"
  type        = string
  default     = "SPOT"

  validation {
    condition     = contains(["SPOT", "ON_DEMAND"], var.node_capacity_type)
    error_message = "Допустимы только SPOT и ON_DEMAND."
  }
}

variable "node_min_size" {
  description = "Минимальное число worker-узлов"
  type        = number
  default     = 1
}

variable "node_max_size" {
  description = "Максимальное число worker-узлов"
  type        = number
  default     = 4
}

variable "node_desired_size" {
  description = "Желаемое число worker-узлов"
  type        = number
  default     = 2
}

variable "postgres_engine_version" {
  description = "Основная версия PostgreSQL в RDS"
  type        = string
  default     = "17"
}

variable "postgres_instance_class" {
  description = "Класс инстанса PostgreSQL"
  type        = string
  default     = "db.t4g.micro"
}

variable "postgres_allocated_storage" {
  description = "Начальный размер диска PostgreSQL в GiB"
  type        = number
  default     = 20
}

variable "postgres_max_allocated_storage" {
  description = "Максимальный размер автоскейлинга диска PostgreSQL в GiB"
  type        = number
  default     = 100
}

variable "postgres_multi_az" {
  description = "Включить Multi-AZ для PostgreSQL"
  type        = bool
  default     = false
}

variable "postgres_backup_retention_days" {
  description = "Срок хранения автоматических резервных копий PostgreSQL"
  type        = number
  default     = 7

  validation {
    condition     = var.postgres_backup_retention_days >= 1 && var.postgres_backup_retention_days <= 35
    error_message = "Срок хранения резервных копий должен быть от 1 до 35 дней."
  }
}

variable "postgres_deletion_protection" {
  description = "Защита PostgreSQL от удаления"
  type        = bool
  default     = false
}

variable "postgres_skip_final_snapshot" {
  description = "Не создавать финальный snapshot при удалении; для production установите false"
  type        = bool
  default     = true
}
