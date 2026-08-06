create table tradingmaster_testnet_orders (
    id bigserial primary key,
    idempotency_key text not null unique,
    request_fingerprint text not null,
    client_order_id text not null unique,
    exchange_order_id bigint,
    symbol text not null,
    side text not null check (side in ('buy', 'sell')),
    order_type text not null check (order_type in ('market', 'limit')),
    quantity text not null,
    price text not null default '',
    mode text not null check (mode in ('validate', 'execute')),
    status text not null check (status in ('reserved', 'validated', 'submitted', 'unknown', 'rejected')),
    exchange_status text not null default '',
    executed_quantity text not null default '',
    cumulative_quote_quantity text not null default '',
    error_code integer,
    error_message text not null default '',
    created_at timestamptz not null,
    updated_at timestamptz not null
);

create index tradingmaster_testnet_orders_created_at_idx
    on tradingmaster_testnet_orders (created_at desc, id desc);

create table tradingmaster_testnet_order_events (
    id bigserial primary key,
    order_id bigint not null references tradingmaster_testnet_orders(id),
    event_type text not null check (event_type in ('reserved', 'validated', 'submitted', 'unknown', 'rejected', 'reconciled')),
    message text not null default '',
    occurred_at timestamptz not null
);

create index tradingmaster_testnet_order_events_order_idx
    on tradingmaster_testnet_order_events (order_id, id);

create or replace function tradingmaster_prevent_testnet_journal_mutation()
returns trigger
language plpgsql
as $$
begin
    raise exception 'журнал тестовой биржи доступен только для добавления';
end;
$$;

create trigger tradingmaster_testnet_events_append_only
before update or delete on tradingmaster_testnet_order_events
for each row execute function tradingmaster_prevent_testnet_journal_mutation();
