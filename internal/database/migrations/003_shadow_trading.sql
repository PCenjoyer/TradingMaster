create table tradingmaster_shadow_candles (
    symbol text not null,
    interval text not null,
    open_time timestamptz not null,
    close_time timestamptz not null,
    open double precision not null check (open > 0),
    high double precision not null check (high > 0),
    low double precision not null check (low > 0),
    close double precision not null check (close > 0),
    volume double precision not null check (volume >= 0),
    primary key (symbol, interval, open_time),
    check (high >= low and high >= open and high >= close and low <= open and low <= close),
    check (close_time >= open_time)
);

create index tradingmaster_shadow_candles_latest_idx
    on tradingmaster_shadow_candles (symbol, interval, open_time desc);

create table tradingmaster_shadow_signals (
    id bigserial primary key,
    symbol text not null,
    interval text not null,
    candle_time timestamptz not null,
    close_price double precision not null check (close_price > 0),
    action text not null check (action in ('hold', 'buy', 'sell')),
    reason text not null,
    atr double precision not null check (atr >= 0),
    in_position_before boolean not null,
    eligible_for_order boolean not null,
    blocked boolean not null,
    block_reason text not null default '',
    order_sent boolean not null default false check (order_sent = false),
    created_at timestamptz not null default clock_timestamp(),
    unique (symbol, interval, candle_time),
    foreign key (symbol, interval, candle_time)
        references tradingmaster_shadow_candles(symbol, interval, open_time)
);

create index tradingmaster_shadow_signals_latest_idx
    on tradingmaster_shadow_signals (candle_time desc, id desc);

create table tradingmaster_shadow_runtime (
    id smallint primary key default 1 check (id = 1),
    leader_id text not null default '',
    connected boolean not null default false,
    last_event_at timestamptz,
    last_candle_at timestamptz,
    last_error text not null default '',
    reconnects bigint not null default 0 check (reconnects >= 0),
    candles_processed bigint not null default 0 check (candles_processed >= 0),
    signals_generated bigint not null default 0 check (signals_generated >= 0),
    updated_at timestamptz
);

insert into tradingmaster_shadow_runtime(id) values (1);

create or replace function tradingmaster_prevent_shadow_mutation()
returns trigger
language plpgsql
as $$
begin
    raise exception 'shadow-журнал доступен только для добавления';
end;
$$;

create trigger tradingmaster_shadow_candles_append_only
before update or delete on tradingmaster_shadow_candles
for each row execute function tradingmaster_prevent_shadow_mutation();

create trigger tradingmaster_shadow_signals_append_only
before update or delete on tradingmaster_shadow_signals
for each row execute function tradingmaster_prevent_shadow_mutation();
