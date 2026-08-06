# Архитектура

## Текущие компоненты

| Компонент | Ответственность |
|---|---|
| domain | Свечи, сигналы, позиции, сделки и результат |
| marketdata | Строгий импорт OHLCV из CSV |
| strategy | Чистая логика Trend Breakout без исполнения |
| risk | Размер позиции, стопы и circuit breaker по просадке |
| safety | Ручной kill switch, дневной лимит и контракт запрета новых позиций |
| database | Пул PostgreSQL и миграции с checksum и advisory lock |
| paper | Тестовый broker, портфель и журнал заявок/исполнений |
| testexchange | Binance Spot Testnet, HMAC-подпись, идемпотентность и reconciliation |
| alert | Telegram-уведомления без влияния сбоя доставки на блокировку |
| backtest | Событийный цикл, комиссии, проскальзывание и статистика |
| httpapi | Health endpoints, API, встроенная русская веб-панель и защита входных данных |
| observability | Prometheus-совместимые метрики |

Стратегия не знает о балансе и бирже. Риск-менеджер не знает о HTTP. Движок связывает их через небольшие интерфейсы, поэтому broker и market-data adapters можно добавлять без переписывания формул стратегии.

Paper broker выполняет заявку, изменение портфеля и safety-проверку в одной PostgreSQL-транзакции. Все paper-операции сериализуются через `SELECT FOR UPDATE` единственной safety-строки. Это намеренно ограничивает пропускную способность, зато дневной лимит не проверяется по устаревшему equity. Закрывающие заявки разрешены даже при активной блокировке, чтобы защита не оставляла позицию без выхода.

Журнал событий заявок и исполнений доступен только для добавления: UPDATE и DELETE отклоняются триггерами PostgreSQL. Текущее состояние заявки хранится отдельно, поэтому чтение не требует сворачивать весь поток событий.

Testnet-адаптер работает только с официальным адресом Binance Spot Testnet. Каждая команда требует `idempotency_key`; из него детерминированно формируется `clientOrderId`. Если ответ matching engine потерян или вернулся HTTP 5xx, адаптер запрашивает заявку по этому ID и сохраняет результат сверки. Резервирование, итоговое состояние и события записываются в общий PostgreSQL, поэтому несколько pod’ов не создают дубли.

Веб-панель компилируется внутрь Go-бинарника через `embed` и обращается к тому же API по same-origin. После проверки `TM_ADMIN_TOKEN` сервер создаёт stateless HMAC-подписанную HttpOnly cookie; секрет CSRF передаётся только в защищённую HTML-страницу. Сессия работает между репликами без sticky sessions, не содержит admin token и становится недействительной после его ротации. Bearer-аутентификация сохранена для CLI и автоматизации.

~~~mermaid
sequenceDiagram
    participant API as Paper API
    participant S as Safety-state
    participant DB as PostgreSQL
    API->>S: Запросить разрешение на BUY
    S->>DB: SELECT safety FOR UPDATE
    API->>DB: Записать заявку, позицию, исполнение и equity
    DB-->>API: COMMIT одной транзакции
    Note over S,DB: Kill switch ждёт завершения уже начатой заявки
~~~

~~~mermaid
sequenceDiagram
    participant API as Testnet API
    participant S as Safety-state
    participant DB as PostgreSQL
    participant B as Binance Spot Testnet
    API->>S: Получить блокировку открытия позиции
    S->>DB: SELECT safety FOR UPDATE
    API->>DB: Зарезервировать idempotency key
    API->>B: Подписанная заявка с clientOrderId
    alt ответ однозначен
        B-->>API: orderId и status
    else timeout или HTTP 5xx
        API->>B: Query order по clientOrderId
        B-->>API: фактическое состояние
    end
    API->>DB: Записать итог и append-only событие
    DB-->>API: COMMIT
~~~

## Последовательность одной свечи

~~~mermaid
sequenceDiagram
    participant B as Движок
    participant F as Исполнение
    participant R as Риск
    participant S as Стратегия
    B->>F: Исполнить отложенный сигнал на Open
    B->>F: Проверить гэп и стоп внутри свечи
    B->>R: Обновить equity и peak
    B->>S: Передать только завершённую историю
    S-->>B: Сигнал для следующей свечи
    B->>R: Поднять trailing stop, но не опускать
~~~

## Путь к реальной торговле

Live-режим нельзя делать простой заменой CSV на WebSocket. Нужны отдельные границы:

1. поток биржевых котировок с контролем устаревших данных;
2. reconciliation балансов и позиций после рестарта;
3. очередь рыночных событий и контроль пропусков sequence ID;
4. outbox для гарантированной доставки событий и уведомлений;
5. отдельные биржевые лимиты и контроль расхождения позиций;
6. алерты по расхождению локальной и биржевой позиции;
7. отдельный проверенный production-адаптер с ручным разрешением и лимитами.

Durable safety-store, paper broker, testnet-адаптер, идемпотентность заявок и reconciliation по `clientOrderId` уже реализованы. При отсутствии PostgreSQL локальный API может работать только в исследовательском режиме: paper trading и testnet отключаются, а статус сообщает `durable_safety_store: false`. `/api/v1/status` продолжает сообщать `live_trading: false`, потому что production API программно запрещён.
