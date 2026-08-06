# Эксплуатация и AWS

## Предварительные требования

- AWS CLI с правами на S3, VPC, IAM, EKS, EC2, ECR, RDS и Secrets Manager;
- Terraform 1.10+;
- kubectl 1.36;
- уникальное имя S3 bucket;
- заранее проверенный бюджет AWS.

EKS, EC2, RDS, Secrets Manager, NAT Gateway и трафик оплачиваются отдельно. `single_nat_gateway = true`, однозонный RDS и Spot nodes уменьшают стоимость dev, но не являются production-конфигурацией. Для production включите Multi-AZ, deletion protection и финальный snapshot. Перед terraform apply обязательно изучите plan.

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

Terraform также создаёт закрытый RDS PostgreSQL. Пароль генерируется RDS и хранится в Secrets Manager, а в Terraform state не записывается как входная переменная.

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

## 6. Настроить PostgreSQL, kill switch и Telegram

Получите endpoint и управляемый RDS secret, соберите URL с корректным percent-encoding и создайте Kubernetes Secret. Команды не записывают пароль в Git:

~~~bash
DB_HOST="$(terraform -chdir=infra/terraform output -raw postgres_endpoint)"
DB_SECRET_ARN="$(terraform -chdir=infra/terraform output -raw postgres_master_secret_arn)"
DB_SECRET_JSON="$(aws secretsmanager get-secret-value \
  --secret-id "$DB_SECRET_ARN" --query SecretString --output text)"
DATABASE_URL="$(DB_HOST="$DB_HOST" DB_SECRET_JSON="$DB_SECRET_JSON" python3 -c '
import json, os, urllib.parse
secret = json.loads(os.environ["DB_SECRET_JSON"])
user = urllib.parse.quote(secret["username"], safe="")
password = urllib.parse.quote(secret["password"], safe="")
host = os.environ["DB_HOST"]
print(f"postgres://{user}:{password}@{host}:5432/tradingmaster?sslmode=require")
')"

kubectl -n tradingmaster create secret generic tradingmaster-secrets \
  --from-literal=database-url="$DATABASE_URL" \
  --from-literal=admin-token="ЗАМЕНИТЕ" \
  --from-literal=telegram-bot-token="ЗАМЕНИТЕ" \
  --from-literal=telegram-chat-id="ЗАМЕНИТЕ" \
  --dry-run=client -o yaml | kubectl apply -f -
unset DATABASE_URL DB_SECRET_JSON DB_SECRET_ARN DB_HOST
~~~

Если Telegram не нужен, не передавайте оба telegram-параметра. `database-url` обязателен для Kubernetes Deployment. При первом создании строки safety-state действует `TM_KILL_SWITCH_DEFAULT=true`; последующие рестарты восстанавливают сохранённое состояние. Несколько pod’ов используют одну блокируемую строку PostgreSQL.

Kubernetes Secret содержит снимок учётных данных. После ротации RDS master secret повторите создание секрета и выполните `kubectl rollout restart deployment/tradingmaster`. Для production лучше подключить External Secrets и отдельного application user с минимальными правами, а миграции запускать отдельной учётной записью.

Пример проверки через port-forward:

~~~bash
export ADMIN_TOKEN="ЗАМЕНИТЕ"
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://localhost:8080/api/v1/safety

curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"action":"deactivate"}' \
  http://localhost:8080/api/v1/safety/kill-switch

curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"equity":10000,"observed_at":"2026-08-06T09:00:00Z"}' \
  http://localhost:8080/api/v1/safety/equity
~~~

Активация требует непустую причину:

~~~bash
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"action":"activate","reason":"аномалия исполнения"}' \
  http://localhost:8080/api/v1/safety/kill-switch
~~~

Дневная автоматическая блокировка не снимается ручной командой в течение того же UTC-дня. Telegram-сбой не откатывает kill switch и виден в метрике tradingmaster_telegram_notifications_total.

## 7. Проверить paper trading

Все paper endpoints требуют тот же Bearer admin token. Покупка открывает или увеличивает позицию, продажа уменьшает её. Активный kill switch отклоняет покупку, но не мешает продаже:

~~~bash
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"symbol":"BTCUSDT","side":"buy","quantity":0.01,"market_price":60000}' \
  http://localhost:8080/api/v1/paper/orders

curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"symbol":"BTCUSDT","price":59000}' \
  http://localhost:8080/api/v1/paper/marks

curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://localhost:8080/api/v1/paper/portfolio
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  'http://localhost:8080/api/v1/paper/orders?limit=50'
~~~

Каждое исполнение атомарно меняет cash и позицию, а затем добавляется в append-only журнал. Рыночная цена передаётся вызывающей стороной: это детерминированный paper-адаптер, а не подключение к биржевому стакану.

## 8. Подключить Prometheus и Grafana

Каталог monitoring требует CRD от Prometheus Operator, например установленного kube-prometheus-stack:

~~~bash
kubectl apply -k monitoring
~~~

ServiceMonitor и PrometheusRule по умолчанию имеют label release=kube-prometheus-stack. Если Helm release называется иначе, замените label перед применением. Grafana sidecar обнаруживает ConfigMap по label grafana_dashboard=1 и загружает dashboard «TradingMaster — безопасность и paper-trading».

Dashboard показывает kill switch, тип safety-store, дневной убыток, paper equity и результаты заявок. Prometheus создаёт critical alert при активной блокировке или локальном safety-store и warning при использовании 80% дневного лимита.

## Ограничение текущей версии

Paper broker не отправляет заявки на биржу и принимает цену исполнения от вызывающей стороны. Пока нет idempotency key, биржевого market-data adapter, reconciliation и outbox, включать live trading нельзя. `/api/v1/status` поэтому продолжает возвращать `live_trading: false`.

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
