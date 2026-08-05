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
