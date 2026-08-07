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

### Встроенная русская панель управления

После `port-forward` откройте http://localhost:8080/ui и войдите значением `TM_ADMIN_TOKEN`. Отдельный frontend-сервис не требуется: HTML, CSS и JavaScript встроены в Go-бинарник и поставляются в том же контейнере.

Панель показывает состояние сервиса, kill switch и дневной лимит, paper-портфель, позиции, журналы заявок, публичный shadow-поток, соединение и тестовые балансы Binance Spot Testnet. Из неё можно:

- включить или снять ручной kill switch;
- создать paper-заявку и обновить виртуальную рыночную цену;
- наблюдать рассчитанные на публичных live-свечах shadow-сигналы и их блокировку safety-контуром;
- отправить заявку только в Binance Spot Testnet и сверить неоднозначный статус.

При входе admin token отправляется серверу формой и не сохраняется в `localStorage` или `sessionStorage`. Сервер выдаёт подписанную HttpOnly cookie со сроком 12 часов. Сессия не хранится в памяти pod и поэтому действует на всех репликах с одинаковым `TM_ADMIN_TOKEN`; ротация токена завершает ранее созданные сессии. Изменяющие запросы дополнительно защищены CSRF-токеном.

В Kubernetes публикуйте панель только через HTTPS ingress. Тогда cookie получает флаг `Secure` по TLS или заголовку `X-Forwarded-Proto: https`. Локальный `http://localhost` оставлен рабочим только для разработки и `port-forward`.

Панель намеренно показывает `LIVE OFF`: публичные котировки не дают доступ к счёту, shadow-пакет не содержит интерфейса отправки заявки, а production endpoint и реальные биржевые ключи не принимаются. Интерфейс не может переключить `/api/v1/status` из `live_trading: false`.

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

## 8. Подключить Binance Spot Testnet

Создайте отдельные ключи на [Binance Spot Test Network](https://testnet.binance.vision/). Не используйте ключи от реального аккаунта: приложение принимает только официальный testnet endpoint, а production URL программно запрещён.

Добавьте тестовые ключи в существующий Kubernetes Secret, не заменяя уже настроенные database/Telegram-значения, и включите безопасный режим `validate`:

~~~bash
BINANCE_TESTNET_API_KEY_B64="$(printf %s "$BINANCE_TESTNET_API_KEY" | base64 | tr -d '\n')"
BINANCE_TESTNET_SECRET_KEY_B64="$(printf %s "$BINANCE_TESTNET_SECRET_KEY" | base64 | tr -d '\n')"
kubectl -n tradingmaster patch secret tradingmaster-secrets --type merge \
  -p "{\"data\":{\"binance-testnet-api-key\":\"$BINANCE_TESTNET_API_KEY_B64\",\"binance-testnet-secret-key\":\"$BINANCE_TESTNET_SECRET_KEY_B64\"}}"
unset BINANCE_TESTNET_API_KEY_B64 BINANCE_TESTNET_SECRET_KEY_B64

kubectl -n tradingmaster patch configmap tradingmaster --type merge \
  -p '{"data":{"TM_BINANCE_TESTNET_ENABLED":"true","TM_BINANCE_TESTNET_ORDER_MODE":"validate"}}'
kubectl -n tradingmaster rollout restart deployment/tradingmaster
kubectl -n tradingmaster rollout status deployment/tradingmaster
~~~

`validate` вызывает `/api/v3/order/test`: биржа проверяет подпись, фильтры символа и параметры, но не передаёт заявку matching engine. После проверки переключите только тестовый контур в `execute`, чтобы заявки исполнялись тестовыми активами:

~~~bash
kubectl -n tradingmaster patch configmap tradingmaster --type merge \
  -p '{"data":{"TM_BINANCE_TESTNET_ORDER_MODE":"execute"}}'
kubectl -n tradingmaster rollout restart deployment/tradingmaster
~~~

Проверка подключения и тестового счёта:

~~~bash
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://localhost:8080/api/v1/testnet/status
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://localhost:8080/api/v1/testnet/account
~~~

Величины передаются строками, чтобы не терять точность десятичных значений. Один `idempotency_key` можно безопасно повторить с теми же параметрами; попытка использовать его для другой заявки вернёт конфликт:

~~~bash
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"idempotency_key":"trend:20260806:0001","symbol":"BTCUSDT","side":"buy","order_type":"market","quantity":"0.001"}' \
  http://localhost:8080/api/v1/testnet/orders

curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  'http://localhost:8080/api/v1/testnet/orders?limit=50'

curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://localhost:8080/api/v1/testnet/orders/trend:20260806:0001/reconcile
~~~

Статус `unknown` означает, что matching engine мог принять заявку, но подтверждение не получено. Не создавайте новую заявку с другим ключом: сначала вызовите reconciliation. Новые покупки проходят через общий kill switch; продажи остаются разрешены для сокращения spot-позиции.

## 9. Включить shadow trading на публичных котировках

Shadow-режим безопасно прогоняет текущую Trend Breakout стратегию на закрытых свечах Binance Spot. Ключи не нужны: начальная история читается через публичный REST API, дальнейшие свечи — через публичный WebSocket. Включение требует PostgreSQL, потому что свечи, сигналы и лидер реплик должны быть durable:

~~~bash
kubectl -n tradingmaster patch configmap tradingmaster --type merge \
  -p '{"data":{"TM_SHADOW_ENABLED":"true","TM_SHADOW_SYMBOLS":"BTCUSDT,ETHUSDT","TM_SHADOW_INTERVAL":"1m","TM_SHADOW_HISTORY_LIMIT":"120"}}'
kubectl -n tradingmaster rollout restart deployment/tradingmaster
kubectl -n tradingmaster rollout status deployment/tradingmaster
~~~

Проверка через admin API:

~~~bash
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://localhost:8080/api/v1/shadow/status
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  'http://localhost:8080/api/v1/shadow/signals?limit=50'
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  'http://localhost:8080/api/v1/shadow/candles?symbol=BTCUSDT&limit=120'
~~~

Допустимы от 1 до 10 символов и интервалы `1m`, `3m`, `5m`, `15m`, `30m`, `1h`, `4h`, `1d`. Только один pod удерживает PostgreSQL advisory lock и подключается к WebSocket; остальные читают общий runtime и готовы стать лидером после сбоя. Таблицы свечей и сигналов append-only. Для каждого сигнала API явно возвращает `order_sent: false`, а ограничение PostgreSQL запрещает сохранить `true`.

## 10. Подключить Prometheus и Grafana

Каталог monitoring требует CRD от Prometheus Operator, например установленного kube-prometheus-stack:

~~~bash
kubectl apply -k monitoring
~~~

ServiceMonitor и PrometheusRule по умолчанию имеют label release=kube-prometheus-stack. Если Helm release называется иначе, замените label перед применением. Grafana sidecar обнаруживает ConfigMap по label grafana_dashboard=1 и загружает dashboard «TradingMaster — безопасность и paper-trading».

Dashboard показывает kill switch, тип safety-store, дневной убыток, paper equity, paper-заявки, состояние shadow-потока и результаты Binance Spot Testnet. Prometheus создаёт critical alert при активной блокировке, локальном safety-store или неизвестном состоянии testnet-заявки; warning — при использовании 80% дневного лимита и отключении включённого shadow-потока.

## Ограничение текущей версии

Paper broker не отправляет заявки на биржу и принимает цену исполнения от вызывающей стороны. Shadow-контур уже получает публичные закрытые свечи, но намеренно не имеет отправки заявок. Testnet-адаптер имеет idempotency key, durable-журнал и reconciliation заявок, однако пока нет сверки всего реального баланса и позиций, контроля sequence gaps стакана и transactional outbox. Включать торговлю реальными средствами нельзя; `/api/v1/status` продолжает возвращать `live_trading: false`.

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
