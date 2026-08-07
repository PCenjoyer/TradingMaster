"use strict";

const csrfToken = document.querySelector('meta[name="csrf-token"]').content;
const money = new Intl.NumberFormat("ru-RU", { maximumFractionDigits: 2 });
const decimal = new Intl.NumberFormat("ru-RU", { maximumFractionDigits: 8 });
let toastTimer;

function element(id) { return document.getElementById(id); }
function setText(id, value) { element(id).textContent = value ?? "—"; }
function yesNo(value) { return value ? "Да" : "Нет"; }
function enabled(value) { return value ? "Включено" : "Отключено"; }
function percent(value) { return `${money.format((Number(value) || 0) * 100)} %`; }
function amount(value) { return Number.isFinite(Number(value)) ? money.format(Number(value)) : "—"; }
function when(value) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString("ru-RU");
}

function setPill(id, label, kind = "") {
  const node = element(id);
  node.textContent = label;
  node.className = `pill ${kind ? `pill-${kind}` : ""}`.trim();
}

function toast(message, isError = false) {
  const node = element("toast");
  node.textContent = message;
  node.className = `toast${isError ? " error" : ""}`;
  node.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { node.hidden = true; }, 5000);
}

async function api(path, options = {}) {
  const method = (options.method || "GET").toUpperCase();
  const headers = new Headers(options.headers || {});
  headers.set("Accept", "application/json");
  if (options.body !== undefined) headers.set("Content-Type", "application/json");
  if (!["GET", "HEAD", "OPTIONS"].includes(method)) headers.set("X-CSRF-Token", csrfToken);
  const response = await fetch(path, { ...options, method, headers, credentials: "same-origin" });
  if (response.status === 401) {
    window.location.assign("/ui/login");
    throw new Error("Сессия завершена");
  }
  let payload = {};
  try { payload = await response.json(); } catch (_) { payload = {}; }
  if (!response.ok) throw new Error(payload.details || payload.error || `Ошибка HTTP ${response.status}`);
  return payload;
}

function emptyRow(tbody, columns, message) {
  const row = document.createElement("tr");
  const cell = document.createElement("td");
  cell.colSpan = columns;
  cell.className = "empty-cell";
  cell.textContent = message;
  row.append(cell);
  tbody.replaceChildren(row);
}

function appendCells(row, values) {
  values.forEach((value) => {
    const cell = document.createElement("td");
    cell.textContent = value ?? "—";
    row.append(cell);
  });
}

function setAvailability(prefix, available, message) {
  element(`${prefix}-disabled`).hidden = available;
  element(`${prefix}-content`).hidden = !available;
  setPill(`${prefix}-pill`, available ? "Включено" : "Отключено", available ? "safe" : "warning");
  if (!available && message) element(`${prefix}-disabled`).textContent = message;
}

function renderStatus(status) {
  setText("service-state", `${status.service} · ${status.version}`);
  setText("service-mode", status.mode);
  setText("kill-state", status.kill_switch_active ? "Торговля заблокирована" : "Защита в норме");
  setText("store-state", status.durable_safety_store && status.safety_store_available ? "Доступно" : "Требует внимания");
  setText("paper-state", enabled(status.paper_trading));
  setText("testnet-state", enabled(status.test_exchange_enabled));
  setText("testnet-mode", status.test_exchange_enabled ? `Режим: ${status.test_exchange_order_mode}` : "Тестовая биржа не настроена");
  setText("shadow-state", enabled(status.shadow_trading));
  setText("shadow-mode", status.shadow_trading ? (status.shadow_connected ? "Поток подключён, заявок: 0" : "Ожидание потока, заявок: 0") : "Публичные котировки отключены");
  setAvailability("paper", Boolean(status.paper_trading));
  setAvailability("testnet", Boolean(status.test_exchange_enabled));
  setAvailability("shadow", Boolean(status.shadow_trading));
}

function renderSafety(state) {
  setText("kill-state", state.active ? "Торговля заблокирована" : "Защита в норме");
  setText("kill-reason", state.reason || "Активных ограничений нет");
  setText("current-equity", amount(state.current_equity));
  setText("start-equity", amount(state.day_start_equity));
  setText("daily-loss", percent(state.daily_loss_ratio));
  setText("daily-limit", percent(state.daily_loss_limit));
  setPill("safety-pill", state.active ? "Блокировка активна" : "Торговля разрешена", state.active ? "danger" : "safe");
}

function renderPaperPortfolio(portfolio) {
  setText("paper-equity", amount(portfolio.equity));
  setText("paper-cash", amount(portfolio.cash));
  setText("paper-position-count", (portfolio.positions || []).length);
  const tbody = element("paper-positions");
  if (!portfolio.positions || portfolio.positions.length === 0) {
    emptyRow(tbody, 5, "Открытых позиций нет");
    return;
  }
  tbody.replaceChildren(...portfolio.positions.map((position) => {
    const row = document.createElement("tr");
    appendCells(row, [position.symbol, decimal.format(position.quantity), amount(position.average_price), amount(position.current_price)]);
    const pnl = document.createElement("td");
    pnl.textContent = amount(position.unrealized_pnl);
    pnl.className = Number(position.unrealized_pnl) >= 0 ? "positive" : "negative";
    row.append(pnl);
    return row;
  }));
}

function renderPaperOrders(payload) {
  const tbody = element("paper-orders");
  const orders = payload.orders || [];
  if (orders.length === 0) {
    emptyRow(tbody, 5, "Заявок пока нет");
    return;
  }
  tbody.replaceChildren(...orders.map((order) => {
    const row = document.createElement("tr");
    appendCells(row, [when(order.created_at), order.symbol, order.side === "buy" ? "Покупка" : "Продажа", decimal.format(order.quantity), order.status]);
    return row;
  }));
}

function renderTestnet(status, account) {
  setText("testnet-connected", yesNo(status.connected));
  setText("testnet-can-trade", yesNo(account.can_trade));
  setText("testnet-order-mode", status.order_mode === "execute" ? "Исполнение в Testnet" : "Только проверка параметров");
  const balances = (account.balances || []).filter((balance) => Number(balance.free) !== 0 || Number(balance.locked) !== 0);
  const tbody = element("testnet-balances");
  if (balances.length === 0) {
    emptyRow(tbody, 3, "Ненулевых тестовых балансов нет");
    return;
  }
  tbody.replaceChildren(...balances.map((balance) => {
    const row = document.createElement("tr");
    appendCells(row, [balance.asset, balance.free, balance.locked]);
    return row;
  }));
}

function renderTestnetOrders(payload) {
  const tbody = element("testnet-orders");
  const orders = payload.orders || [];
  if (orders.length === 0) {
    emptyRow(tbody, 5, "Заявок пока нет");
    return;
  }
  tbody.replaceChildren(...orders.map((order) => {
    const row = document.createElement("tr");
    appendCells(row, [when(order.created_at), order.symbol, order.order_type, order.status]);
    const action = document.createElement("td");
    if (["unknown", "submitted", "reserved"].includes(order.status)) {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "button button-secondary button-small";
      button.textContent = "Сверить";
      button.addEventListener("click", () => reconcile(order.idempotency_key));
      action.append(button);
    } else {
      action.textContent = "—";
    }
    row.append(action);
    return row;
  }));
}

function shadowAction(action) {
  if (action === "buy") return "Купить";
  if (action === "sell") return "Продать";
  return "Наблюдать";
}

function renderShadow(status, payload) {
  setText("shadow-connected", status.connected ? (status.leader ? "Подключено · лидер" : "Подключено · общий поток") : "Нет соединения");
  setText("shadow-symbols", `${(status.symbols || []).join(", ")} · ${status.interval}`);
  setText("shadow-last-candle", status.last_candle_at ? when(status.last_candle_at) : "История загружается");
  setText("shadow-orders-sent", status.orders_sent ?? 0);
  setPill("shadow-pill", status.connected ? "Поток подключён" : "Ожидание потока", status.connected ? "safe" : "warning");
  const tbody = element("shadow-signals");
  const signals = payload.signals || [];
  if (signals.length === 0) {
    emptyRow(tbody, 7, "Сигналы появятся после загрузки истории и закрытия свечи");
    return;
  }
  tbody.replaceChildren(...signals.map((signal) => {
    const row = document.createElement("tr");
    appendCells(row, [
      when(signal.candle_time), signal.symbol, amount(signal.close_price),
      shadowAction(signal.action), signal.reason,
      signal.blocked ? `Заблокировано: ${signal.block_reason || "safety"}` : (signal.eligible_for_order ? "Прошёл проверки" : "Не требуется"),
      signal.order_sent ? "Отправлена" : "Не отправлялась",
    ]);
    return row;
  }));
}

async function loadAll(silent = false) {
  if (!silent) setText("global-message", "Обновляем данные…");
  try {
    const [status, safety] = await Promise.all([api("/api/v1/status"), api("/api/v1/safety")]);
    renderStatus(status);
    renderSafety(safety);
    const tasks = [];
    if (status.paper_trading) {
      tasks.push(Promise.all([api("/api/v1/paper/portfolio"), api("/api/v1/paper/orders?limit=20")])
        .then(([portfolio, orders]) => { renderPaperPortfolio(portfolio); renderPaperOrders(orders); }));
    } else {
      emptyRow(element("paper-orders"), 5, "Paper trading отключён");
    }
    if (status.test_exchange_enabled) {
      tasks.push(Promise.all([api("/api/v1/testnet/status"), api("/api/v1/testnet/account"), api("/api/v1/testnet/orders?limit=20")])
        .then(([testnetStatus, account, orders]) => { renderTestnet(testnetStatus, account); renderTestnetOrders(orders); }));
    } else {
      emptyRow(element("testnet-orders"), 5, "Binance Testnet отключён");
    }
    if (status.shadow_trading) {
      tasks.push(Promise.all([api("/api/v1/shadow/status"), api("/api/v1/shadow/signals?limit=30")])
        .then(([shadowStatus, signals]) => renderShadow(shadowStatus, signals)));
    } else {
      emptyRow(element("shadow-signals"), 7, "Shadow trading отключён");
    }
    const results = await Promise.allSettled(tasks);
    const failed = results.find((result) => result.status === "rejected");
    if (failed) throw failed.reason;
    setText("last-update", `Обновлено ${new Date().toLocaleTimeString("ru-RU")}`);
    setText("global-message", "Все данные актуальны");
  } catch (error) {
    setText("global-message", error.message);
    if (!silent) toast(error.message, true);
  }
}

async function submitJSON(path, body, success) {
  try {
    await api(path, { method: "POST", body: JSON.stringify(body) });
    toast(success);
    await loadAll(true);
  } catch (error) {
    toast(error.message, true);
  }
}

async function reconcile(key) {
  await submitJSON(`/api/v1/testnet/orders/${encodeURIComponent(key)}/reconcile`, {}, "Заявка сверена с Testnet");
}

function newIdempotencyKey() {
  const suffix = globalThis.crypto?.randomUUID?.().replaceAll("-", "").slice(0, 16) || Math.random().toString(36).slice(2, 18);
  return `ui-${Date.now()}-${suffix}`;
}

element("refresh").addEventListener("click", () => loadAll());
element("logout").addEventListener("click", async () => {
  try {
    await api("/ui/logout", { method: "POST", body: JSON.stringify({}) });
    window.location.assign("/ui/login");
  } catch (error) { toast(error.message, true); }
});

element("kill-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const action = event.submitter?.value || "activate";
  const reason = new FormData(event.currentTarget).get("reason").trim();
  if (action === "activate" && !reason) {
    toast("Укажите причину аварийной остановки", true);
    return;
  }
  submitJSON("/api/v1/safety/kill-switch", { action, reason }, action === "activate" ? "Kill switch активирован" : "Ручная блокировка снята");
});

element("paper-order-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const data = new FormData(event.currentTarget);
  submitJSON("/api/v1/paper/orders", {
    symbol: data.get("symbol").trim().toUpperCase(), side: data.get("side"),
    quantity: Number(data.get("quantity")), market_price: Number(data.get("market_price")),
  }, "Виртуальная заявка обработана");
});

element("paper-mark-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const data = new FormData(event.currentTarget);
  submitJSON("/api/v1/paper/marks", {
    symbol: data.get("symbol").trim().toUpperCase(), price: Number(data.get("price")),
  }, "Виртуальная цена обновлена");
});

element("testnet-order-type").addEventListener("change", (event) => {
  const isLimit = event.target.value === "limit";
  element("testnet-price-field").hidden = !isLimit;
  element("testnet-price-field").querySelector("input").required = isLimit;
});

element("testnet-order-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const data = new FormData(event.currentTarget);
  const body = {
    idempotency_key: data.get("idempotency_key").trim(),
    symbol: data.get("symbol").trim().toUpperCase(), side: data.get("side"),
    order_type: data.get("order_type"), quantity: data.get("quantity").trim(),
  };
  if (body.order_type === "limit") body.price = data.get("price").trim();
  submitJSON("/api/v1/testnet/orders", body, "Тестовая заявка обработана");
  element("idempotency-key").value = newIdempotencyKey();
});

element("idempotency-key").value = newIdempotencyKey();
loadAll();
setInterval(() => loadAll(true), 15000);
