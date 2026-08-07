create table tradingmaster_safety_state (
    id smallint primary key default 1 check (id = 1),
    manual_active boolean not null,
    manual_reason text not null,
    daily_limit_active boolean not null,
    trading_day text not null,
    day_start_equity double precision not null check (day_start_equity >= 0),
    current_equity double precision not null check (current_equity >= 0),
    daily_loss_ratio double precision not null check (daily_loss_ratio >= 0),
    daily_loss_limit double precision not null check (daily_loss_limit > 0 and daily_loss_limit < 1),
    updated_at timestamptz,
    last_equity_at timestamptz
);

create table tradingmaster_paper_accounts (
    id smallint primary key default 1 check (id = 1),
    cash double precision not null check (cash >= 0),
    initial_capital double precision not null check (initial_capital > 0),
    updated_at timestamptz not null
);

create table tradingmaster_paper_market_prices (
    symbol text primary key,
    price double precision not null check (price > 0),
    observed_at timestamptz not null
);

create table tradingmaster_paper_positions (
    symbol text primary key,
    quantity double precision not null check (quantity > 0),
    average_price double precision not null check (average_price > 0),
    updated_at timestamptz not null
);

create table tradingmaster_paper_orders (
    id text primary key,
    created_at timestamptz not null,
    symbol text not null,
    side text not null check (side in ('buy', 'sell')),
    order_type text not null check (order_type = 'market'),
    quantity double precision not null check (quantity > 0),
    market_price double precision not null check (market_price > 0),
    status text not null check (status in ('created', 'filled', 'rejected')),
    reject_reason text not null default ''
);

create index tradingmaster_paper_orders_created_at_idx
    on tradingmaster_paper_orders (created_at desc, id desc);

create table tradingmaster_paper_order_events (
    id bigserial primary key,
    order_id text not null references tradingmaster_paper_orders(id),
    event_type text not null check (event_type in ('created', 'filled', 'rejected')),
    reason text not null default '',
    occurred_at timestamptz not null
);

create index tradingmaster_paper_order_events_order_idx
    on tradingmaster_paper_order_events (order_id, id);

create table tradingmaster_paper_executions (
    id text primary key,
    order_id text not null unique references tradingmaster_paper_orders(id),
    quantity double precision not null check (quantity > 0),
    price double precision not null check (price > 0),
    commission double precision not null check (commission >= 0),
    executed_at timestamptz not null
);

create or replace function tradingmaster_prevent_journal_mutation()
returns trigger
language plpgsql
as $$
begin
    raise exception 'журнал paper-trading доступен только для добавления';
end;
$$;

create trigger tradingmaster_paper_events_append_only
before update or delete on tradingmaster_paper_order_events
for each row execute function tradingmaster_prevent_journal_mutation();

create trigger tradingmaster_paper_executions_append_only
before update or delete on tradingmaster_paper_executions
for each row execute function tradingmaster_prevent_journal_mutation();
