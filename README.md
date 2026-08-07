# TradingMaster

TradingMaster — собственный движок алгоритмической торговли на Go. Текущая версия предназначена для исследования стратегий, воспроизводимых бэктестов, paper-trading, Binance Spot Testnet и shadow-проверки сигналов на публичных live-котировках. Отправка заявок с реальными средствами намеренно запрещена.

> Важно: проект не обещает доходность и не является инвестиционной рекомендацией. Результаты на истории не гарантируют будущий результат. Сначала используйте только исторические данные и тестовый счёт.

## Что уже работает

- event-driven бэктест без look-ahead: сигнал формируется на закрытии, исполняется на следующем открытии;
- собственная трендовая стратегия: Donchian breakout, SMA-фильтр и ATR;
- размер позиции по допустимому риску, ATR-стоп, trailing stop и ограничение просадки;
- ручной kill switch и автоматический дневной лимит убытка с блокировкой новых позиций;
- защищённый admin API, fail-safe запуск и Telegram-уведомления о переключениях защиты;
- общий durable safety-store в PostgreSQL с атомарной координацией нескольких экземпляров;
- paper broker с рыночными заявками, портфелем и append-only журналом заявок и исполнений;
- Binance Spot Testnet с HMAC-подписью, идемпотентными client order ID, reconciliation и durable-журналом;
- shadow trading на публичных свечах Binance Spot через WebSocket: backfill, переподключение, выбор одного лидера между репликами и append-only журнал сигналов без интерфейса отправки заявок;
- встроенная русская веб-панель без React: safety-контроль, paper-портфель и Binance Spot Testnet в том же Go-бинарнике;
- комиссии, проскальзывание и гэпы через стоп;
- метрики: доходность, максимальная просадка, Sharpe, profit factor, win rate и экспозиция;
- загрузка OHLCV из CSV и HTTP API для запуска бэктестов;
- Prometheus-совместимые метрики, alert rules и готовый Grafana dashboard;
- минимальный non-root контейнер, Docker Compose и защищённые Kubernetes-манифесты;
- Terraform для VPC, Amazon EKS, RDS PostgreSQL, managed nodes, ECR и защищённого S3 state;
- CI для Go, контейнера, Kubernetes и Terraform;
- CD в GHCR по тегам и ручное развёртывание выбранной версии в EKS через GitHub OIDC.

## Быстрый запуск

Требуется Go 1.25+.

~~~bash
go test ./...
go run ./cmd/tradingmaster -mode api
~~~

Для локального safety API скопируйте [.env.example](.env.example) в файл .env и задайте длинный случайный admin token. Docker Compose подхватит файл автоматически.

После запуска:

- GET http://localhost:8080/ui — русская панель управления с защищённой сессией;
- GET http://localhost:8080/healthz — жив ли процесс;
- GET http://localhost:8080/readyz — готов ли сервис;
- GET http://localhost:8080/api/v1/status — режим и версия;
- POST http://localhost:8080/api/v1/backtests — бэктест массива свечей;
- GET http://localhost:8080/api/v1/safety — состояние защиты, требуется Bearer admin token;
- POST http://localhost:8080/api/v1/safety/kill-switch — ручная блокировка или разблокировка;
- POST http://localhost:8080/api/v1/safety/equity — обновление equity для дневного лимита;
- POST http://localhost:8080/api/v1/paper/orders — отправка paper-заявки;
- GET http://localhost:8080/api/v1/paper/orders — журнал заявок и исполнений;
- GET http://localhost:8080/api/v1/paper/portfolio — текущий тестовый портфель;
- POST http://localhost:8080/api/v1/paper/marks — обновление рыночной цены и safety equity;
- GET http://localhost:8080/api/v1/testnet/status — связь и время Binance Spot Testnet;
- GET http://localhost:8080/api/v1/testnet/account — тестовый счёт и ненулевые балансы;
- POST http://localhost:8080/api/v1/testnet/orders — валидация или отправка тестовой заявки;
- GET http://localhost:8080/api/v1/testnet/orders — durable-журнал тестовых заявок;
- POST http://localhost:8080/api/v1/testnet/orders/{idempotency_key}/reconcile — сверка неоднозначной заявки;
- GET http://localhost:8080/api/v1/shadow/status — состояние публичного market-data потока;
- GET http://localhost:8080/api/v1/shadow/signals — последние shadow-сигналы;
- GET http://localhost:8080/api/v1/shadow/candles?symbol=BTCUSDT — сохранённые закрытые свечи;
- GET http://localhost:8080/metrics — метрики.

Откройте http://localhost:8080/ui и войдите значением `TM_ADMIN_TOKEN`. Токен проверяется только при входе: браузер получает подписанную HttpOnly session cookie на 12 часов, а сам токен не записывается в `localStorage` или `sessionStorage`. Из панели можно управлять kill switch, видеть дневной лимит, работать с paper-портфелем и Binance Spot Testnet, а также наблюдать live shadow-сигналы. Реальная торговля в интерфейсе отсутствует и программно отключена.

Чтобы включить безопасное наблюдение за рынком, задайте в `.env` `TM_SHADOW_ENABLED=true`. Контур использует только публичные данные, не принимает API-ключи и всегда возвращает `orders_sent: 0`.

Запуск в контейнере:

~~~bash
docker compose up --build
~~~

Compose поднимает приложение и PostgreSQL. Схема применяется автоматически под advisory lock; повторный запуск сохраняет safety-state, портфель и журнал в named volume.

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
6. после просадки 15% новые входы блокируются до перезапуска тестового контура;
7. после дневного убытка 3% новые входы блокируются до следующего UTC-дня.

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
    U["Русская веб-панель"] --> A
    M --> A
    P["Paper broker"] --> J["PostgreSQL: safety и журнал"]
    A --> P
    A --> T["Binance Spot Testnet"]
    T --> J
    W["Binance public WebSocket"] --> H["Shadow trading: только сигналы"]
    H --> J
    H --> A
~~~

Границы компонентов и путь к live-trading описаны в [docs/architecture.md](docs/architecture.md).

## Облачная инфраструктура

Terraform создаёт:

- VPC в двух Availability Zones;
- public/private subnets и NAT Gateway;
- EKS 1.36 с закрытым API endpoint по умолчанию;
- managed node group на Spot-инстансах для dev;
- ECR с immutable tags, шифрованием, сканированием и lifecycle policy;
- закрытый RDS PostgreSQL с шифрованием, резервными копиями и паролем под управлением Secrets Manager;
- отдельный S3 bucket для versioned и locked Terraform state.

Kubernetes запускает от двух до шести экземпляров приложения с общим PostgreSQL safety-state, restricted Pod Security, non-root UID, read-only root filesystem, удалёнными Linux capabilities, resource limits, probes, PDB, HPA по CPU и памяти и NetworkPolicy. Без обязательного секрета `database-url` pod не запускается.

Опциональный каталог [monitoring](monitoring) содержит ServiceMonitor, PrometheusRule и русский Grafana dashboard «TradingMaster — безопасность и paper-trading».

Порядок подготовки AWS, оценка расходов и развёртывание: [docs/operations.md](docs/operations.md).

## Разработка

~~~bash
gofmt -w ./cmd ./internal
go test ./...
go vet ./...
go build -trimpath ./cmd/tradingmaster
kubectl kustomize deploy/k8s
kubectl kustomize monitoring
~~~

CI запускает аналогичные проверки на каждом PR и поднимает PostgreSQL для интеграционных тестов миграций, блокировок, paper broker, testnet-идемпотентности, shadow safety-инвариантов и append-only журналов. Тег v* публикует multi-arch образ в ghcr.io/pcenjoyer/tradingmaster. Развёртывание запускается вручную workflow «Развёртывание в EKS», чтобы случайный push не менял production.

## Безопасность и источники

- Секреты не хранятся в Git: AWS-доступ для CD выдаётся краткоживущим OIDC-токеном.
- Database URL, admin token, Telegram credentials и тестовые биржевые ключи загружаются только из окружения или Kubernetes Secret.
- Веб-панель использует HttpOnly SameSite=Strict cookie, CSRF-токен и строгую Content Security Policy; admin token не сохраняется в браузерном хранилище.
- Testnet-адаптер принимает только `https://testnet.binance.vision`; production endpoint нельзя включить переменной окружения.
- Shadow-контур принимает рыночные данные только с официальных публичных Binance REST/WebSocket endpoint и не содержит broker-интерфейса.
- Kubernetes-профиль следует официальному [Restricted Pod Security Standard](https://kubernetes.io/docs/concepts/security/pod-security-standards/).
- Terraform state использует [S3 locking через lockfile](https://developer.hashicorp.com/terraform/language/backend/s3), а бакет имеет versioning и запрет публичного доступа.
- Подключение к закрытому EKS описано в [документации AWS](https://docs.aws.amazon.com/eks/latest/userguide/create-kubeconfig.html).
- CFTC отдельно предупреждает, что торговые боты не способны гарантировать доходность: [Customer Advisory](https://www.cftc.gov/LearnAndProtect/AdvisoriesAndArticles/AITradingBots.html).

Проект распространяется по лицензии MIT. См. [LICENSE](LICENSE). Уведомления о лицензиях встроенных Go-модулей находятся в [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) и копируются в контейнерный образ.
