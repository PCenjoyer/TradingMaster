variable "aws_region" {
  description = "Регион AWS для хранилища состояния"
  type        = string
  default     = "eu-central-1"
}

variable "state_bucket_name" {
  description = "Глобально уникальное имя S3-бакета Terraform state"
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$", var.state_bucket_name))
    error_message = "Имя бакета должно соответствовать правилам AWS S3."
  }
}

output "state_bucket_name" {
  description = "Имя созданного S3-бакета"
  value       = aws_s3_bucket.terraform_state.id
}
