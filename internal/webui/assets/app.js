"use strict";

const state = {
  apiKey: "",
  principal: null,
  endpoints: [],
  deliveries: [],
  deliveryStatus: "",
  activeView: "overview",
};

const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => Array.from(root.querySelectorAll(selector));

const viewMeta = {
  overview: ["Центр управления", "Обзор"],
  endpoints: ["Получатели", "Точки назначения"],
  deliveries: ["Журнал доставки", "Доставки"],
  send: ["Создание события", "Отправить событие"],
};

function setBusy(button, busy, text) {
  if (!button) return;
  if (!button.dataset.label) button.dataset.label = button.textContent;
  button.disabled = busy;
  button.textContent = busy ? text : button.dataset.label;
}

async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  headers.set("Authorization", `Bearer ${state.apiKey}`);
  if (options.body) headers.set("Content-Type", "application/json");
  const response = await fetch(path, { ...options, headers });
  if (response.status === 204) return null;
  const contentType = response.headers.get("content-type") || "";
  const body = contentType.includes("application/json") ? await response.json() : null;
  if (!response.ok) {
    const error = new Error(body?.error?.message || `Ошибка API: ${response.status}`);
    error.status = response.status;
    throw error;
  }
  return body;
}

function toast(message, kind = "") {
  const item = document.createElement("div");
  item.className = `toast ${kind}`;
  item.textContent = message;
  $("#toast-region").append(item);
  setTimeout(() => item.remove(), 3600);
}

function shortID(value) {
  if (!value) return "—";
  return value.length > 16 ? `${value.slice(0, 8)}…${value.slice(-5)}` : value;
}

function formatDate(value, withTime = true) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return new Intl.DateTimeFormat("ru-RU", {
    day: "2-digit",
    month: "short",
    hour: withTime ? "2-digit" : undefined,
    minute: withTime ? "2-digit" : undefined,
  }).format(date);
}

function statusLabel(status) {
  return {
    pending: "В очереди",
    delivering: "Доставка",
    succeeded: "Успешно",
    retrying: "Повтор",
    dead: "Dead letter",
  }[status] || status;
}

function statusHTML(status) {
  return `<span class="status status-${escapeHTML(status)}">${escapeHTML(statusLabel(status))}</span>`;
}

function escapeHTML(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

async function login(event) {
  event.preventDefault();
  const button = $("#login-button");
  const error = $("#login-error");
  const key = $("#api-key").value.trim();
  error.textContent = "";
  setBusy(button, true, "Проверяем подключение…");
  state.apiKey = key;
  try {
    state.principal = await api("/v1/me");
    $("#api-key").value = "";
    $("#login-view").hidden = true;
    $("#app-view").hidden = false;
    hydratePrincipal();
    regenerateIdempotencyKey();
    await loadAll();
  } catch (err) {
    state.apiKey = "";
    error.textContent = err.status === 401
      ? "Ключ не принят. Проверьте значение в .env и попробуйте снова."
      : "Не удалось подключиться к HookForge. Проверьте, что сервис запущен.";
  } finally {
    setBusy(button, false);
  }
}

function logout() {
  state.apiKey = "";
  state.principal = null;
  state.endpoints = [];
  state.deliveries = [];
  $("#api-key").value = "";
  $("#app-view").hidden = true;
  $("#login-view").hidden = false;
  $("#login-error").textContent = "";
}

function hydratePrincipal() {
  const tenant = state.principal?.tenant || {};
  $("#tenant-name").textContent = tenant.name || "Организация";
  $("#tenant-slug").textContent = tenant.slug ? `@${tenant.slug}` : shortID(tenant.id);
  $("#tenant-avatar").textContent = (tenant.name || "HF")
    .split(/\s+/)
    .slice(0, 2)
    .map(part => part[0])
    .join("")
    .toUpperCase();
}

async function loadAll(showToast = false) {
  const button = $("#refresh-button");
  setBusy(button, true, "Обновляем…");
  try {
    const [endpoints, deliveries] = await Promise.all([
      api("/v1/endpoints?limit=200"),
      api("/v1/deliveries?limit=200"),
    ]);
    state.endpoints = endpoints.data || [];
    state.deliveries = deliveries.data || [];
    renderAll();
    if (showToast) toast("Данные обновлены");
  } catch (err) {
    if (err.status === 401) {
      logout();
      $("#login-error").textContent = "Срок действия подключения закончился. Введите ключ снова.";
    } else {
      toast(err.message, "error");
    }
  } finally {
    setBusy(button, false);
  }
}

async function loadDeliveries(status = state.deliveryStatus) {
  try {
    const query = status ? `?limit=200&status=${encodeURIComponent(status)}` : "?limit=200";
    const body = await api(`/v1/deliveries${query}`);
    state.deliveries = body.data || [];
    renderDeliveriesTable();
  } catch (err) {
    toast(err.message, "error");
  }
}

function renderAll() {
  renderStats();
  renderRecentDeliveries();
  renderOverviewEndpoints();
  renderEndpointsTable();
  renderDeliveriesTable();
  renderEndpointChecklist();
}

function renderStats() {
  const counts = state.deliveries.reduce((result, item) => {
    result[item.status] = (result[item.status] || 0) + 1;
    return result;
  }, {});
  const total = state.deliveries.length;
  const successRate = total ? Math.round(((counts.succeeded || 0) / total) * 100) : 0;
  $("#stat-endpoints").textContent = state.endpoints.length;
  $("#stat-enabled").textContent = `${state.endpoints.filter(item => item.enabled).length} активных`;
  $("#stat-success").textContent = total ? `${successRate}%` : "0%";
  $("#stat-queue").textContent = (counts.pending || 0) + (counts.delivering || 0) + (counts.retrying || 0);
  $("#stat-dead").textContent = counts.dead || 0;
}

function renderRecentDeliveries() {
  const root = $("#recent-deliveries");
  root.classList.remove("loading-block");
  const items = state.deliveries.slice(0, 5);
  if (!items.length) {
    root.innerHTML = `<div class="empty-state"><span class="empty-icon">↗</span><h3>Доставок пока нет</h3><p>Отправьте первое событие.</p></div>`;
    return;
  }
  root.innerHTML = items.map(item => `
    <div class="delivery-row">
      ${statusHTML(item.status)}
      <code title="${escapeHTML(item.event_id)}">${escapeHTML(shortID(item.event_id))}</code>
      <span>${item.last_status_code || "—"}</span>
      <span class="delivery-time">${escapeHTML(formatDate(item.updated_at))}</span>
    </div>
  `).join("");
}

function renderOverviewEndpoints() {
  const root = $("#overview-endpoints");
  root.classList.remove("loading-block");
  const items = state.endpoints.slice(0, 5);
  if (!items.length) {
    root.innerHTML = `<div class="empty-state"><span class="empty-icon">◎</span><h3>Нет точек назначения</h3><p>Добавьте первый адрес.</p></div>`;
    return;
  }
  root.innerHTML = items.map(item => `
    <div class="mini-endpoint">
      <span class="mini-endpoint-icon">◎</span>
      <span><strong>${escapeHTML(item.name)}</strong><small>${escapeHTML(item.url)}</small></span>
      <i title="${item.enabled ? "Активен" : "Отключён"}"></i>
    </div>
  `).join("");
}

function renderEndpointsTable() {
  const table = $("#endpoints-table");
  const empty = $("#endpoints-empty");
  empty.hidden = state.endpoints.length > 0;
  table.innerHTML = state.endpoints.map(item => `
    <tr>
      <td><div class="cell-primary"><strong>${escapeHTML(item.name)}</strong><small>Получатель вебхука</small></div></td>
      <td class="url-cell" title="${escapeHTML(item.url)}">${escapeHTML(item.url)}</td>
      <td><span class="status ${item.enabled ? "status-succeeded" : "status-dead"}">${item.enabled ? "Активен" : "Отключён"}</span></td>
      <td>${escapeHTML(formatDate(item.created_at, false))}</td>
      <td><code class="id-cell" title="${escapeHTML(item.id)}">${escapeHTML(shortID(item.id))}</code></td>
    </tr>
  `).join("");
}

function renderDeliveriesTable() {
  const table = $("#deliveries-table");
  const empty = $("#deliveries-empty");
  empty.hidden = state.deliveries.length > 0;
  table.innerHTML = state.deliveries.map(item => `
    <tr>
      <td>${statusHTML(item.status)}</td>
      <td><code class="id-cell" title="${escapeHTML(item.event_id)}">${escapeHTML(shortID(item.event_id))}</code></td>
      <td><code class="id-cell" title="${escapeHTML(item.endpoint_id)}">${escapeHTML(shortID(item.endpoint_id))}</code></td>
      <td>${item.attempt_count}</td>
      <td>${item.last_status_code || "—"}</td>
      <td>${escapeHTML(formatDate(item.updated_at))}</td>
      <td>${item.status === "dead" ? `<button class="row-action" type="button" data-replay="${escapeHTML(item.id)}">Повторить</button>` : ""}</td>
    </tr>
  `).join("");
}

function renderEndpointChecklist() {
  const root = $("#endpoint-checklist");
  if (!state.endpoints.length) {
    root.innerHTML = `<p class="field-help invalid">Сначала создайте хотя бы одну точку назначения.</p>`;
    return;
  }
  root.innerHTML = state.endpoints.filter(item => item.enabled).map(item => `
    <label class="endpoint-option">
      <input type="checkbox" name="endpoint" value="${escapeHTML(item.id)}">
      <span><strong>${escapeHTML(item.name)}</strong><small>${escapeHTML(item.url)}</small></span>
    </label>
  `).join("");
}

function switchView(view) {
  if (!viewMeta[view]) return;
  state.activeView = view;
  $$(".view").forEach(item => item.classList.toggle("active-view", item.id === `view-${view}`));
  $$(".nav-item[data-view]").forEach(item => item.classList.toggle("active", item.dataset.view === view));
  $("#page-kicker").textContent = viewMeta[view][0];
  $("#page-title").textContent = viewMeta[view][1];
  $(".sidebar").classList.remove("open");
  if (view === "deliveries") loadDeliveries();
}

function openEndpointDialog() {
  $("#endpoint-error").textContent = "";
  $("#endpoint-dialog").showModal();
  setTimeout(() => $("#endpoint-name").focus(), 20);
}

async function createEndpoint(event) {
  event.preventDefault();
  const button = $("#create-endpoint-button");
  const error = $("#endpoint-error");
  error.textContent = "";
  setBusy(button, true, "Создаём…");
  try {
    const body = await api("/v1/endpoints", {
      method: "POST",
      body: JSON.stringify({
        name: $("#endpoint-name").value.trim(),
        url: $("#endpoint-url").value.trim(),
      }),
    });
    $("#endpoint-dialog").close();
    $("#endpoint-form").reset();
    $("#created-secret").textContent = body.secret;
    $("#secret-dialog").showModal();
    await loadAll();
  } catch (err) {
    error.textContent = err.message;
  } finally {
    setBusy(button, false);
  }
}

function regenerateIdempotencyKey() {
  const value = globalThis.crypto?.randomUUID?.() ||
    `evt-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  $("#idempotency-key").value = value;
}

function validateJSON() {
  const status = $("#json-status");
  try {
    JSON.parse($("#event-payload").value);
    status.textContent = "Корректный JSON";
    status.classList.remove("invalid");
    return true;
  } catch (err) {
    status.textContent = `Ошибка JSON: ${err.message}`;
    status.classList.add("invalid");
    return false;
  }
}

async function sendEvent(event) {
  event.preventDefault();
  const selected = $$('input[name="endpoint"]:checked').map(input => input.value);
  if (!selected.length) {
    toast("Выберите хотя бы одну точку назначения", "error");
    return;
  }
  if (!validateJSON()) return;
  const button = $("#send-event-button");
  setBusy(button, true, "Сохраняем событие…");
  try {
    const body = await api("/v1/events", {
      method: "POST",
      headers: { "Idempotency-Key": $("#idempotency-key").value.trim() },
      body: JSON.stringify({
        type: $("#event-type").value.trim(),
        payload: JSON.parse($("#event-payload").value),
        endpoint_ids: selected,
      }),
    });
    $("#result-event-id").textContent = body.event.id;
    $("#result-event-id").title = body.event.id;
    $("#result-duplicate").textContent = body.duplicate ? "Дубликат — возвращено существующее" : "Новое — принято в доставку";
    $("#event-result").hidden = false;
    toast(body.duplicate ? "Событие уже существовало" : "Событие принято в доставку");
    regenerateIdempotencyKey();
    await loadAll();
  } catch (err) {
    toast(err.message, "error");
  } finally {
    setBusy(button, false);
  }
}

async function replayDelivery(id, button) {
  setBusy(button, true, "…");
  try {
    await api(`/v1/deliveries/${encodeURIComponent(id)}/replay`, { method: "POST" });
    toast("Доставка возвращена в очередь");
    await loadDeliveries();
  } catch (err) {
    toast(err.message, "error");
  } finally {
    setBusy(button, false);
  }
}

async function copyText(value, successMessage) {
  try {
    await navigator.clipboard.writeText(value);
    toast(successMessage);
  } catch {
    toast("Не удалось скопировать автоматически", "error");
  }
}

function bindEvents() {
  $("#login-form").addEventListener("submit", login);
  $("#toggle-key").addEventListener("click", () => {
    const input = $("#api-key");
    const visible = input.type === "text";
    input.type = visible ? "password" : "text";
    $("#toggle-key").textContent = visible ? "Показать" : "Скрыть";
  });
  $("#logout-button").addEventListener("click", logout);
  $("#refresh-button").addEventListener("click", () => loadAll(true));
  $("#menu-button").addEventListener("click", () => $(".sidebar").classList.toggle("open"));
  $$(".nav-item[data-view]").forEach(button => button.addEventListener("click", () => switchView(button.dataset.view)));
  $$("[data-view-jump]").forEach(button => button.addEventListener("click", () => switchView(button.dataset.viewJump)));
  $("#new-endpoint-button").addEventListener("click", openEndpointDialog);
  $$("[data-open-endpoint]").forEach(button => button.addEventListener("click", openEndpointDialog));
  $$("[data-close-dialog]").forEach(button => button.addEventListener("click", () => $("#endpoint-dialog").close()));
  $$("[data-close-secret]").forEach(button => button.addEventListener("click", () => $("#secret-dialog").close()));
  $("#endpoint-form").addEventListener("submit", createEndpoint);
  $("#copy-secret").addEventListener("click", () => copyText($("#created-secret").textContent, "Signing secret скопирован"));
  $("#event-form").addEventListener("submit", sendEvent);
  $("#event-payload").addEventListener("input", validateJSON);
  $("#regenerate-key").addEventListener("click", regenerateIdempotencyKey);
  $("#copy-event-id").addEventListener("click", () => copyText($("#result-event-id").textContent, "ID события скопирован"));
  $("#status-filters").addEventListener("click", event => {
    const button = event.target.closest("[data-status]");
    if (!button) return;
    state.deliveryStatus = button.dataset.status;
    $$(".filter", $("#status-filters")).forEach(item => item.classList.toggle("active", item === button));
    loadDeliveries();
  });
  $("#deliveries-table").addEventListener("click", event => {
    const button = event.target.closest("[data-replay]");
    if (button) replayDelivery(button.dataset.replay, button);
  });
  $("#endpoint-dialog").addEventListener("click", event => {
    if (event.target === $("#endpoint-dialog")) $("#endpoint-dialog").close();
  });
  $("#secret-dialog").addEventListener("cancel", event => event.preventDefault());
}

bindEvents();
