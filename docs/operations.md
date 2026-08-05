# Эксплуатация и AWS

## Предварительные требования

- AWS CLI с правами на S3, VPC, IAM, EKS, EC2 и ECR;
- Terraform 1.10+;
- kubectl 1.36;
- уникальное имя S3 bucket;
- заранее проверенный бюджет AWS.

EKS, EC2, NAT Gateway и трафик оплачиваются отдельно. single_nat_gateway = true и Spot nodes уменьшают стоимость dev, но не являются production-конфигурацией. Перед terraform apply обязательно изучите plan.

## 1. Создать хранилище Terraform state

~~~bash
cp infra/bootstrap/terraform.tfvars.example infra/bootstrap/terraform.tfvars
terraform -chdir=infra/bootstrap init
terraform -chdir=infra/bootstrap plan
terraform -chdir=infra/bootstrap apply
~~~

Замените значение state_bucket_name на глобально уникальное. Бакет получает versioning, server-side encryption, запрет публичного доступа и prevent_destroy.

## 2. Подготовить окружение

~~~bash
cp infra/terraform/backend.hcl.example infra/terraform/backend.hcl
cp infra/terraform/terraform.tfvars.example infra/terraform/terraform.tfvars
terraform -chdir=infra/terraform init -backend-config=backend.hcl
terraform -chdir=infra/terraform plan -out=dev.tfplan
terraform -chdir=infra/terraform apply dev.tfplan
~~~

По умолчанию Kubernetes API закрыт снаружи. Для администрирования используйте среду внутри VPC или AWS CloudShell с VPC environment. Временный публичный доступ разрешайте только конкретному доверенному /32, никогда не 0.0.0.0/0.

После создания:

~~~bash
aws eks update-kubeconfig --region eu-central-1 --name tradingmaster-dev
kubectl get nodes
~~~

## 3. Опубликовать версию

Создайте и отправьте тег:

~~~bash
git tag v0.1.0
git push origin v0.1.0
~~~

Workflow «Публикация контейнера» соберёт amd64/arm64 образ, SBOM и provenance и отправит образ в GHCR. Для публичного кластера сделайте container package публичным либо заранее создайте imagePullSecret.

## 4. Настроить безопасный CD

В GitHub Environment production задайте variables:

- AWS_DEPLOY_ROLE_ARN — IAM role с trust policy для GitHub OIDC;
- AWS_REGION — например eu-central-1;
- EKS_CLUSTER_NAME — например tradingmaster-dev.

Добавьте protection rule с ручным подтверждением. Поскольку EKS API закрыт по умолчанию, deploy runner должен находиться внутри VPC и иметь labels self-hosted, linux, x64. Выделите этот runner только для доверенных deployment jobs. Не храните постоянные AWS access keys в GitHub Secrets.

После этого вручную запустите workflow «Развёртывание в EKS» и передайте существующий immutable image tag.

## 5. Проверка

~~~bash
kubectl -n tradingmaster get deploy,pods,svc,hpa,pdb
kubectl -n tradingmaster rollout status deployment/tradingmaster
kubectl -n tradingmaster port-forward service/tradingmaster 8080:80
~~~

В другом терминале:

~~~bash
curl http://localhost:8080/healthz
curl http://localhost:8080/api/v1/status
curl http://localhost:8080/metrics
~~~

## Откат

~~~bash
kubectl -n tradingmaster rollout history deployment/tradingmaster
kubectl -n tradingmaster rollout undo deployment/tradingmaster
kubectl -n tradingmaster rollout status deployment/tradingmaster
~~~

Для удаления dev-инфраструктуры сначала экспортируйте важные данные и изучите план:

~~~bash
terraform -chdir=infra/terraform plan -destroy
terraform -chdir=infra/terraform destroy
~~~

State bucket защищён prevent_destroy и удаляется отдельно только осознанно.
