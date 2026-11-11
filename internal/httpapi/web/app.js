"use strict";

const state = {
  apiKey: "",
  links: [],
  activeStatsCode: "",
};

const elements = {
  apiKey: document.querySelector("#api-key"),
  connect: document.querySelector("#connect-button"),
  notice: document.querySelector("#notice"),
  createForm: document.querySelector("#create-form"),
  refresh: document.querySelector("#refresh-button"),
  list: document.querySelector("#link-list"),
  statsPanel: document.querySelector("#stats-panel"),
  statsCode: document.querySelector("#stats-code"),
  closeStats: document.querySelector("#close-stats"),
  totalClicks: document.querySelector("#total-clicks"),
  uniqueVisitors: document.querySelector("#unique-visitors"),
  dailyChart: document.querySelector("#daily-chart"),
  deviceList: document.querySelector("#device-list"),
  referrerList: document.querySelector("#referrer-list"),
};

function notify(message, isError = false) {
  elements.notice.textContent = message;
  elements.notice.classList.toggle("error", isError);
  elements.notice.hidden = false;
  window.clearTimeout(notify.timer);
  notify.timer = window.setTimeout(() => {
    elements.notice.hidden = true;
  }, 5000);
}

async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  if (state.apiKey) {
    headers.set("Authorization", `Bearer ${state.apiKey}`);
  }
  if (options.body) {
    headers.set("Content-Type", "application/json");
  }

  const response = await fetch(path, { ...options, headers });
  if (response.status === 204) {
    return null;
  }
  const payload = await response.json().catch(() => null);
  if (!response.ok) {
    const message = payload?.error?.message || `请求失败（HTTP ${response.status}）`;
    throw new Error(message);
  }
  return payload;
}

function requireAPIKey() {
  if (!state.apiKey) {
    notify("请先输入 API Key 并连接。", true);
    return false;
  }
  return true;
}

async function loadLinks() {
  if (!requireAPIKey()) {
    return;
  }
  try {
    const payload = await api("/api/v1/links?limit=100");
    state.links = payload.items || [];
    renderLinks();
  } catch (error) {
    notify(error.message, true);
  }
}

function renderLinks() {
  elements.list.replaceChildren();
  if (state.links.length === 0) {
    const row = document.createElement("tr");
    const cell = document.createElement("td");
    cell.colSpan = 4;
    cell.className = "empty-state";
    cell.textContent = "还没有短链，先创建一个。";
    row.append(cell);
    elements.list.append(row);
    return;
  }

  for (const link of state.links) {
    const row = document.createElement("tr");

    const codeCell = document.createElement("td");
    const codeLink = document.createElement("a");
    codeLink.className = "short-code";
    codeLink.href = link.short_url;
    codeLink.target = "_blank";
    codeLink.rel = "noopener noreferrer";
    codeLink.textContent = link.code;
    codeCell.append(codeLink);

    const targetCell = document.createElement("td");
    const target = document.createElement("span");
    target.className = "target-url";
    target.title = link.target_url;
    target.textContent = link.target_url;
    targetCell.append(target);

    const statusCell = document.createElement("td");
    const status = document.createElement("span");
    status.className = `status${link.is_active ? "" : " off"}`;
    status.textContent = link.is_active ? "启用" : "停用";
    statusCell.append(status);

    const actionsCell = document.createElement("td");
    const actions = document.createElement("div");
    actions.className = "row-actions";
    actions.append(
      actionButton("统计", () => showStats(link.code)),
      actionButton(link.is_active ? "停用" : "启用", () => toggleLink(link)),
      actionButton("删除", () => deleteLink(link.code), "danger"),
    );
    actionsCell.append(actions);

    row.append(codeCell, targetCell, statusCell, actionsCell);
    elements.list.append(row);
  }
}

function actionButton(label, handler, extraClass = "") {
  const button = document.createElement("button");
  button.type = "button";
  button.className = `button small ghost ${extraClass}`.trim();
  button.textContent = label;
  button.addEventListener("click", handler);
  return button;
}

async function createLink(event) {
  event.preventDefault();
  if (!requireAPIKey()) {
    return;
  }

  const form = new FormData(elements.createForm);
  const payload = {
    target_url: String(form.get("target_url") || "").trim(),
  };
  const alias = String(form.get("custom_alias") || "").trim();
  const expires = String(form.get("expires_at") || "");
  if (alias) {
    payload.custom_alias = alias;
  }
  if (expires) {
    payload.expires_at = new Date(expires).toISOString();
  }

  try {
    const created = await api("/api/v1/links", {
      method: "POST",
      body: JSON.stringify(payload),
    });
    elements.createForm.reset();
    notify(`已创建：${created.short_url}`);
    await loadLinks();
  } catch (error) {
    notify(error.message, true);
  }
}

async function toggleLink(link) {
  try {
    await api(`/api/v1/links/${encodeURIComponent(link.code)}`, {
      method: "PATCH",
      body: JSON.stringify({ is_active: !link.is_active }),
    });
    await loadLinks();
  } catch (error) {
    notify(error.message, true);
  }
}

async function deleteLink(code) {
  if (!window.confirm(`确定删除短码 ${code}？统计明细会保留。`)) {
    return;
  }
  try {
    await api(`/api/v1/links/${encodeURIComponent(code)}`, { method: "DELETE" });
    if (state.activeStatsCode === code) {
      elements.statsPanel.hidden = true;
    }
    notify("短链已删除。");
    await loadLinks();
  } catch (error) {
    notify(error.message, true);
  }
}

async function showStats(code) {
  if (!requireAPIKey()) {
    return;
  }
  try {
    const stats = await api(`/api/v1/links/${encodeURIComponent(code)}/stats`);
    state.activeStatsCode = code;
    elements.statsCode.textContent = code;
    elements.totalClicks.textContent = stats.total_clicks;
    elements.uniqueVisitors.textContent = stats.unique_visitors;
    renderChart(stats.daily || []);
    renderRanking(elements.deviceList, stats.devices || []);
    renderRanking(elements.referrerList, stats.referrers || []);
    elements.statsPanel.hidden = false;
    elements.statsPanel.scrollIntoView({ behavior: "smooth", block: "start" });
  } catch (error) {
    notify(error.message, true);
  }
}

function renderChart(days) {
  elements.dailyChart.replaceChildren();
  const max = Math.max(1, ...days.map((item) => item.clicks));
  for (const day of days) {
    const column = document.createElement("div");
    column.className = "bar-column";
    column.title = `${day.date}: ${day.clicks} 次点击`;

    const bar = document.createElement("div");
    bar.className = "bar";
    bar.style.height = `${Math.max(2, (day.clicks / max) * 145)}px`;

    const label = document.createElement("span");
    label.className = "bar-label";
    label.textContent = day.date.slice(5);
    column.append(bar, label);
    elements.dailyChart.append(column);
  }
}

function renderRanking(container, items) {
  container.replaceChildren();
  if (items.length === 0) {
    const empty = document.createElement("li");
    empty.textContent = "暂无数据";
    container.append(empty);
    return;
  }
  for (const item of items) {
    const row = document.createElement("li");
    const name = document.createElement("span");
    const clicks = document.createElement("span");
    name.textContent = item.name || "unknown";
    clicks.textContent = item.clicks;
    row.append(name, clicks);
    container.append(row);
  }
}

elements.connect.addEventListener("click", async () => {
  state.apiKey = elements.apiKey.value.trim();
  if (!state.apiKey) {
    notify("请输入 API Key。", true);
    return;
  }
  await loadLinks();
  if (state.links.length > 0) {
    notify("连接成功，已加载链接。");
  }
});

elements.refresh.addEventListener("click", loadLinks);
elements.createForm.addEventListener("submit", createLink);
elements.closeStats.addEventListener("click", () => {
  elements.statsPanel.hidden = true;
});
