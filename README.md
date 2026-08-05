# TradingMaster

TradingMaster — собственный движок алгоритмической торговли на Go. Текущая версия предназначена для исследования стратегий, воспроизводимых бэктестов и подготовки к paper-trading. Отправка реальных заявок на биржу намеренно отключена до появления отдельного адаптера, тестового контура и аварийных ограничителей.

> Важно: проект не обещает доходность и не является инвестиционной рекомендацией. Результаты на истории не гарантируют будущий результат. Сначала используйте только исторические данные и тестовый счёт.

## Что уже работает

- event-driven бэктест без look-ahead: сигнал формируется на закрытии, исполняется на следующем открытии;
- собственная трендовая стратегия: Donchian breakout, SMA-фильтр и ATR;
- размер позиции по допустимому риску, ATR-стоп, trailing stop и ограничение просадки;
- комиссии, проскальзывание и гэпы через стоп;
- метрики: доходность, максимальная просадка, Sharpe, profit factor, win rate и экспозиция;
- загрузка OHLCV из CSV и HTTP API для запуска бэктестов;
- Prometheus-совместимые метрики и health/readiness endpoints;
- минимальный non-root контейнер, Docker Compose и защищённые Kubernetes-манифесты;
- Terraform для VPC, Amazon EKS, managed nodes, ECR и защищённого S3 state;
- CI для Go, контейнера, Kubernetes и Terraform;
- CD в GHCR по тегам и ручное развёртывание выбранной версии в EKS через GitHub OIDC.

## Быстрый запуск

Требуется Go 1.25+.

~~~bash
go test ./...
go run ./cmd/tradingmaster -mode api
~~~

После запуска:

- GET http://localhost:8080/healthz — жив ли процесс;
- GET http://localhost:8080/readyz — готов ли сервис;
- GET http://localhost:8080/api/v1/status — режим и версия;
- POST http://localhost:8080/api/v1/backtests — бэктест массива свечей;
- GET http://localhost:8080/metrics — метрики.

Запуск в контейнере:

~~~bash
docker compose up --build
~~~

Запуск бэктеста из CSV:

~~~bash
go run ./cmd/tradingmaster -mode backtest -data ./data/BTCUSDT-1d.csv
~~~

CSV должен иметь заголовок:

~~~text
timestamp,open,high,low,close,volume
~~~

Поле timestamp принимает RFC3339, Unix seconds или Unix milliseconds. Свечи должны идти строго по возрастанию времени.

## Стратегия

Стратегия вдохновлена публичными трендовыми и Turtle-подходами, но собрана как самостоятельная комбинация:

1. вход только после пробоя максимума предыдущих 20 свечей;
2. фильтр направления: цена выше SMA(50);
3. волатильность и защитный стоп рассчитываются через ATR(14);
4. выход — пробой минимума предыдущих 10 свечей либо trailing stop;
5. базовый риск — 1% капитала на сделку, позиция — не более 25% капитала;
6. после просадки 15% новые входы блокируются до перезапуска тестового контура.

Параметры API настраиваются в каждом запросе. Подробные формулы, допущения и ограничения: [docs/strategy.md](docs/strategy.md).

## Архитектура

~~~mermaid
flowchart LR
    D["CSV / массив OHLCV"] --> E["Event-driven движок"]
    E --> S["Стратегия"]
    S --> R["Риск-менеджер"]
    R --> F["Модель исполнения"]
    F --> M["Сделки и метрики"]
    A["HTTP API"] --> E
    M --> A
~~~

Границы компонентов и путь к live-trading описаны в [docs/architecture.md](docs/architecture.md).

## Облачная инфраструктура

Terraform создаёт:

- VPC в двух Availability Zones;
- public/private subnets и NAT Gateway;
- EKS 1.36 с закрытым API endpoint по умолчанию;
- managed node group на Spot-инстансах для dev;
- ECR с immutable tags, шифрованием, сканированием и lifecycle policy;
- отдельный S3 bucket для versioned и locked Terraform state.

Kubernetes запускает два экземпляра приложения с restricted Pod Security, non-root UID, read-only root filesystem, удалёнными Linux capabilities, resource limits, probes, PDB, HPA и NetworkPolicy.

Порядок подготовки AWS, оценка расходов и развёртывание: [docs/operations.md](docs/operations.md).

## Разработка

~~~bash
gofmt -w ./cmd ./internal
go test ./...
go vet ./...
go build -trimpath ./cmd/tradingmaster
kubectl kustomize deploy/k8s
~~~

CI запускает аналогичные проверки на каждом PR. Тег v* публикует multi-arch образ в ghcr.io/pcenjoyer/tradingmaster. Развёртывание запускается вручную workflow «Развёртывание в EKS», чтобы случайный push не менял production.

## Безопасность и источники

- Секреты не хранятся в Git: AWS-доступ для CD выдаётся краткоживущим OIDC-токеном.
- Kubernetes-профиль следует официальному [Restricted Pod Security Standard](https://kubernetes.io/docs/concepts/security/pod-security-standards/).
- Terraform state использует [S3 locking через lockfile](https://developer.hashicorp.com/terraform/language/backend/s3), а бакет имеет versioning и запрет публичного доступа.
- Подключение к закрытому EKS описано в [документации AWS](https://docs.aws.amazon.com/eks/latest/userguide/create-kubeconfig.html).
- CFTC отдельно предупреждает, что торговые боты не способны гарантировать доходность: [Customer Advisory](https://www.cftc.gov/LearnAndProtect/AdvisoriesAndArticles/AITradingBots.html).

Проект распространяется по лицензии MIT. См. [LICENSE](LICENSE).
