"use strict";

const state = { source: null, snapshot: null, selectedPlugin: "", interactionKind: "", streamCount: 0, actionPending: false };
const viewCopy = {
  overview: ["运行总览", "观察插件进程、连接与业务健康的实时状态。"],
  instances: ["插件控制", "启动、停止或重启插件实例，状态变化由实时流回显。"],
  interactions: ["消息交互", "分别查看 Master 请求响应与 Plugin 主动上报。"],
  events: ["生命周期", "追踪注册、启停、更新、崩溃与自动恢复事件。"],
  insights: ["数据洞察", "按实例汇总交互、延迟、主动上报和重启数据。"],
  deploy: ["版本更新", "上传插件二进制，验证候选实例后动态切换。"]
};

const $ = selector => document.querySelector(selector);
const elements = {
  pluginCount: $("#plugin-count"), aliveCount: $("#alive-count"), healthyCount: $("#healthy-count"), streamCount: $("#stream-count"),
  observedAt: $("#observed-at"), connectionLabel: $("#connection-label"), liveBadge: $(".live-badge"), streamDetail: $("#stream-detail"),
  pluginFilter: $("#plugin-filter"), pluginsBody: $("#plugins-body"), fleetCards: $("#fleet-cards"), liveFeed: $("#live-feed"), liveCount: $("#live-count"),
  interactions: $("#interactions"), events: $("#events"), eventCount: $("#event-count"), footerState: $("#footer-state"), reconnect: $("#reconnect"),
  viewTitle: $("#view-title"), viewDescription: $("#view-description"), insightEmpty: $("#insight-empty"), insightContent: $("#insight-content"),
  insightMessages: $("#insight-messages"), insightReports: $("#insight-reports"), insightLatency: $("#insight-latency"), insightRestarts: $("#insight-restarts"), snapshotDetails: $("#snapshot-details"), restartRecords: $("#restart-records"),
  uploadForm: $("#upload-form"), uploadPlugin: $("#upload-plugin"), uploadVersion: $("#upload-version"), uploadBinary: $("#upload-binary"), uploadSubmit: $("#upload-submit"), fileLabel: $("#file-label"), toast: $("#toast")
};

function escapeHTML(value) {
  return String(value ?? "").replace(/[&<>'"]/g, character => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;" })[character]);
}

function formatTime(value) {
  if (!value || String(value).startsWith("0001-")) return "--";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return "--";
  return new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }).format(parsed);
}

function stateClass(value) { return String(value ?? "").toLowerCase().replace(/[^a-z]/g, ""); }
function filtered(values) { return state.selectedPlugin ? values.filter(value => value.plugin_id === state.selectedPlugin) : values; }

function showToast(message, error = false) {
  elements.toast.textContent = message;
  elements.toast.classList.toggle("error", error);
  elements.toast.hidden = false;
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => { elements.toast.hidden = true; }, 4200);
}

function setConnection(online) {
  elements.liveBadge.classList.toggle("online", online);
  elements.liveBadge.classList.toggle("offline", !online);
  elements.connectionLabel.textContent = online ? "实时流已连接" : "实时流重连中";
  elements.streamDetail.textContent = online ? "SSE / CONNECTED" : "SSE / RETRYING";
  elements.footerState.textContent = online ? "SSE CONNECTED" : "SSE RECONNECTING";
}

function switchView(name) {
  const selected = viewCopy[name] ? name : "overview";
  document.querySelectorAll(".nav-item").forEach(button => button.classList.toggle("active", button.dataset.view === selected));
  document.querySelectorAll("[data-view-panel]").forEach(panel => {
    const active = panel.dataset.viewPanel === selected;
    panel.hidden = !active;
    panel.classList.toggle("active", active);
  });
  [elements.viewTitle.textContent, elements.viewDescription.textContent] = viewCopy[selected];
  if (window.location.hash !== `#${selected}`) history.replaceState(null, "", `#${selected}`);
}

function updateSelectors(plugins) {
  const ids = plugins.map(plugin => plugin.plugin_id);
  if (state.selectedPlugin && !ids.includes(state.selectedPlugin)) state.selectedPlugin = "";
  const uploadSelection = elements.uploadPlugin.value;
  const options = ['<option value="">全部插件</option>', ...ids.map(id => `<option value="${escapeHTML(id)}" ${id === state.selectedPlugin ? "selected" : ""}>${escapeHTML(id)}</option>`)].join("");
  elements.pluginFilter.innerHTML = options;
  elements.uploadPlugin.innerHTML = ['<option value="">选择运行中的插件</option>', ...plugins.filter(plugin => plugin.process_alive).map(plugin => `<option value="${escapeHTML(plugin.plugin_id)}">${escapeHTML(plugin.plugin_id)} · ${escapeHTML(plugin.version)}</option>`)].join("");
  if (ids.includes(uploadSelection)) elements.uploadPlugin.value = uploadSelection;
}

function renderMetrics(snapshot) {
  elements.pluginCount.textContent = snapshot.plugin_count ?? 0;
  elements.aliveCount.textContent = snapshot.process_alive_count ?? 0;
  elements.healthyCount.textContent = snapshot.healthy_count ?? 0;
  elements.streamCount.textContent = state.streamCount;
  elements.observedAt.textContent = formatTime(snapshot.observed_at);
}

function renderFleet(plugins) {
  const visible = filtered(plugins);
  if (!visible.length) { elements.fleetCards.innerHTML = '<p class="empty">暂无插件实例</p>'; return; }
  elements.fleetCards.innerHTML = visible.map(plugin => `<article class="fleet-card">
    <div class="fleet-name"><span>${escapeHTML(plugin.plugin_id)}</span><span class="state ${stateClass(plugin.observed_state)}">${escapeHTML(plugin.observed_state)}</span></div>
    <p class="fleet-meta">PID ${escapeHTML(plugin.pid || "—")} · v${escapeHTML(plugin.version)} · gen ${escapeHTML(plugin.generation)}</p>
    <div class="health-line" title="进程 / RPC / 业务健康"><span class="${plugin.process_alive ? "ok" : ""}"></span><span class="${plugin.rpc_connected ? "ok" : ""}"></span><span class="${plugin.healthy ? "ok" : ""}"></span></div>
  </article>`).join("");
}

function renderPlugins(plugins, binaries) {
  const visible = filtered(plugins);
  const available = state.selectedPlugin ? [] : binaries.filter(binary => !(binary.registered_ids ?? []).length);
  if (!visible.length && !available.length) { elements.pluginsBody.innerHTML = '<tr><td colspan="7" class="empty">插件目录中没有可用程序</td></tr>'; return; }
  const instances = visible.map(plugin => {
    const running = plugin.process_alive;
    return `<tr><td><span class="plugin-name">${escapeHTML(plugin.plugin_id)}</span><span class="plugin-meta">PID ${escapeHTML(plugin.pid || "—")}</span></td>
      <td>${escapeHTML(plugin.version || "—")}<span class="plugin-meta">GEN ${escapeHTML(plugin.generation)}</span></td>
      <td><span class="status ${running ? "ok" : ""}">${running ? "ALIVE" : "DOWN"}</span></td>
      <td><span class="status ${plugin.rpc_connected ? "ok" : ""}">${plugin.rpc_connected ? "CONNECTED" : "OFFLINE"}</span></td>
      <td><span class="status ${plugin.healthy ? "ok" : "warn"}">${plugin.healthy ? "HEALTHY" : "NOT READY"}</span></td>
      <td><span class="state-pair"><span class="state ${stateClass(plugin.desired_state)}">${escapeHTML(plugin.desired_state)}</span>→<span class="state ${stateClass(plugin.observed_state)}">${escapeHTML(plugin.observed_state)}</span></span></td>
      <td><div class="row-actions"><button class="action-button" data-action="start" data-id="${escapeHTML(plugin.plugin_id)}" ${running ? "disabled" : ""}>启动</button><button class="action-button danger" data-action="stop" data-id="${escapeHTML(plugin.plugin_id)}" ${running ? "" : "disabled"}>停止</button><button class="action-button" data-action="restart" data-id="${escapeHTML(plugin.plugin_id)}">重启</button></div></td></tr>`;
  }).join("");
  const catalog = available.map((binary, index) => `<tr class="catalog-row"><td><span class="plugin-name">${escapeHTML(binary.type)}-${escapeHTML(binary.version)}</span><span class="plugin-meta">磁盘可用程序</span></td>
    <td>${escapeHTML(binary.version)}<span class="plugin-meta">${escapeHTML(binary.type)}</span></td><td><span class="status available">AVAILABLE</span></td><td>—</td><td>—</td><td><span class="state">UNREGISTERED</span></td>
    <td><button class="action-button" data-action="register-start" data-index="${index}">注册并启动</button></td></tr>`).join("");
  elements.pluginsBody.innerHTML = instances + catalog;
}

function interactionHTML(item) {
  const report = item.kind === "ACTIVE_REPORT";
  const failed = item.status !== "SUCCESS";
  return `<article class="timeline-item"><time class="timeline-time">${formatTime(item.completed_at || item.sent_at)}</time><div><div class="timeline-title"><span class="tag ${report ? "report" : ""} ${failed ? "failed" : ""}">${escapeHTML(item.kind)}</span><span>${escapeHTML(item.message_type)}</span><span class="direction">${escapeHTML(item.direction)}</span></div><p class="timeline-detail">${escapeHTML(item.plugin_id)} · gen ${escapeHTML(item.generation)} · ${escapeHTML(item.duration_ms)}ms · ${escapeHTML(item.status)}</p><details><summary>查看载荷</summary><pre>${escapeHTML(JSON.stringify({ request: item.request_payload, response: item.response_payload, error: item.error }, null, 2))}</pre></details></div></article>`;
}

function eventHTML(event) {
  return `<article class="timeline-item"><time class="timeline-time">${formatTime(event.occurred_at)}</time><div><div class="timeline-title"><span class="tag">EVENT</span><span>${escapeHTML(event.event_type)}</span></div><p class="timeline-detail">${escapeHTML(event.plugin_id || "MASTER")}${event.details ? ` · ${escapeHTML(JSON.stringify(event.details))}` : ""}</p></div></article>`;
}

function renderHistories(snapshot) {
  const interactions = filtered(snapshot.interactions ?? []).filter(item => !state.interactionKind || item.kind === state.interactionKind);
  elements.interactions.innerHTML = interactions.length ? [...interactions].reverse().map(interactionHTML).join("") : '<p class="empty">暂无消息交互</p>';
  const events = filtered(snapshot.events ?? []);
  elements.eventCount.textContent = events.length;
  elements.events.innerHTML = events.length ? [...events].reverse().map(eventHTML).join("") : '<p class="empty">暂无生命周期事件</p>';
  const liveItems = [
    ...filtered(snapshot.interactions ?? []).map(value => ({ time: value.completed_at || value.sent_at, html: interactionHTML(value) })),
    ...filtered(snapshot.events ?? []).map(value => ({ time: value.occurred_at, html: eventHTML(value) }))
  ].sort((a, b) => new Date(b.time) - new Date(a.time)).slice(0, 12);
  elements.liveCount.textContent = liveItems.length;
  elements.liveFeed.innerHTML = liveItems.length ? liveItems.map(item => item.html).join("") : '<p class="empty">等待事件…</p>';
}

function renderInsights(snapshot) {
  const plugin = snapshot.plugins.find(value => value.plugin_id === state.selectedPlugin);
  elements.insightEmpty.hidden = Boolean(plugin);
  elements.insightContent.hidden = !plugin;
  if (!plugin) return;
  const interactions = (snapshot.interactions ?? []).filter(value => value.plugin_id === plugin.plugin_id);
  const reports = interactions.filter(value => value.kind === "ACTIVE_REPORT").length;
  const latency = interactions.length ? Math.round(interactions.reduce((sum, value) => sum + Number(value.duration_ms || 0), 0) / interactions.length) : 0;
  const restarts = (snapshot.restarts ?? []).filter(value => value.plugin_id === plugin.plugin_id);
  elements.insightMessages.textContent = interactions.length;
  elements.insightReports.textContent = reports;
  elements.insightLatency.innerHTML = `${latency}<span class="unit">ms</span>`;
  elements.insightRestarts.textContent = plugin.restart_count ?? restarts.length;
  const details = [["插件 ID",plugin.plugin_id],["版本",plugin.version],["PID",plugin.pid || "—"],["Generation",plugin.generation],["Observed state",plugin.observed_state],["最后健康检查",formatTime(plugin.last_health_check_at)],["最后交互",formatTime(plugin.last_interaction_at)],["最后错误",plugin.last_error || "—"]];
  elements.snapshotDetails.innerHTML = details.map(([key,value]) => `<dt>${escapeHTML(key)}</dt><dd>${escapeHTML(value)}</dd>`).join("");
  elements.restartRecords.innerHTML = restarts.length ? [...restarts].reverse().map(record => `<article class="timeline-item"><time class="timeline-time">${formatTime(record.completed_at)}</time><div><div class="timeline-title"><span class="tag ${record.success ? "report" : "failed"}">${record.success ? "SUCCESS" : "FAILED"}</span><span>第 ${escapeHTML(record.attempt)} 次重启</span></div><p class="timeline-detail">PID ${escapeHTML(record.old_pid)} → ${escapeHTML(record.new_pid || "—")} · ${escapeHTML(record.reason)}${record.error ? ` · ${escapeHTML(record.error)}` : ""}</p></div></article>`).join("") : '<p class="empty">暂无重启记录</p>';
}

function render(snapshot) {
  state.snapshot = snapshot;
  updateSelectors(snapshot.plugins ?? []);
  renderMetrics(snapshot);
  renderFleet(snapshot.plugins ?? []);
  renderPlugins(snapshot.plugins ?? [], snapshot.available_binaries ?? []);
  renderHistories(snapshot);
  renderInsights(snapshot);
}

function connectStream() {
  if (state.source) state.source.close();
  setConnection(false);
  const source = new EventSource("/api/monitor/stream");
  state.source = source;
  source.addEventListener("snapshot", event => { state.streamCount += 1; setConnection(true); render(JSON.parse(event.data)); });
  source.addEventListener("heartbeat", () => setConnection(true));
  source.addEventListener("stream_error", event => { setConnection(true); showToast(JSON.parse(event.data).error || "实时数据读取失败", true); });
  source.addEventListener("error", () => setConnection(false));
}

async function lifecycleAction(id, action) {
  if (state.actionPending) return;
  if (["stop","restart"].includes(action) && !window.confirm(`确认${action === "stop" ? "停止" : "重启"}插件 ${id}？`)) return;
  state.actionPending = true;
  try {
    const response = await fetch(`/api/plugins/${encodeURIComponent(id)}/${action}`, { method: "POST" });
    const body = response.status === 204 ? {} : await response.json();
    if (!response.ok) throw new Error(body.error || `${response.status} ${response.statusText}`);
    showToast(`插件 ${id} ${action === "start" ? "启动" : action === "stop" ? "停止" : "重启"}成功`);
  } catch (error) { showToast(error.message, true); } finally { state.actionPending = false; }
}

async function registerAndStart(index) {
  if (state.actionPending) return;
  const binary = (state.snapshot?.available_binaries ?? []).filter(value => !(value.registered_ids ?? []).length)[index];
  if (!binary) return;
  const id = `${binary.type}-${binary.version}`;
  state.actionPending = true;
  try {
    const registered = await fetch("/api/plugins", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id, type: binary.type, version: binary.version, binary_path: binary.binary_path }) });
    const registerBody = await registered.json();
    if (!registered.ok) throw new Error(registerBody.error || `${registered.status} ${registered.statusText}`);
    const started = await fetch(`/api/plugins/${encodeURIComponent(id)}/start`, { method: "POST" });
    const startBody = await started.json();
    if (!started.ok) throw new Error(`插件已注册，但启动失败：${startBody.error || started.statusText}`);
    showToast(`插件 ${id} 已注册并启动`);
  } catch (error) { showToast(error.message, true); } finally { state.actionPending = false; }
}

async function uploadUpdate(event) {
  event.preventDefault();
  const id = elements.uploadPlugin.value;
  const file = elements.uploadBinary.files[0];
  if (!id || !file) return;
  const form = new FormData();
  form.append("version", elements.uploadVersion.value);
  form.append("binary", file);
  elements.uploadSubmit.disabled = true;
  elements.uploadSubmit.textContent = "上传并验证中…";
  try {
    const response = await fetch(`/api/plugins/${encodeURIComponent(id)}/update/upload`, { method: "POST", body: form });
    const body = await response.json();
    if (!response.ok) throw new Error(body.error || `${response.status} ${response.statusText}`);
    showToast(`插件 ${id} 已更新到 ${elements.uploadVersion.value}`);
    elements.uploadForm.reset();
    elements.fileLabel.textContent = "选择文件，最大 100 MiB";
  } catch (error) { showToast(error.message, true); } finally {
    elements.uploadSubmit.disabled = false;
    elements.uploadSubmit.textContent = "上传、验证并切换";
  }
}

document.querySelectorAll(".nav-item").forEach(button => button.addEventListener("click", () => switchView(button.dataset.view)));
elements.pluginFilter.addEventListener("change", event => { state.selectedPlugin = event.target.value; if (state.snapshot) render(state.snapshot); });
elements.reconnect.addEventListener("click", connectStream);
elements.pluginsBody.addEventListener("click", event => {
  const button = event.target.closest("button[data-action]");
  if (!button) return;
  if (button.dataset.action === "register-start") registerAndStart(Number(button.dataset.index));
  else lifecycleAction(button.dataset.id, button.dataset.action);
});
document.querySelectorAll("#interaction-kind button").forEach(button => button.addEventListener("click", () => { state.interactionKind = button.dataset.kind; document.querySelectorAll("#interaction-kind button").forEach(item => item.classList.toggle("active", item === button)); if (state.snapshot) renderHistories(state.snapshot); }));
elements.uploadBinary.addEventListener("change", () => { elements.fileLabel.textContent = elements.uploadBinary.files[0]?.name || "选择文件，最大 100 MiB"; });
elements.uploadForm.addEventListener("submit", uploadUpdate);
window.addEventListener("hashchange", () => switchView(window.location.hash.slice(1)));

switchView(window.location.hash.slice(1));
connectStream();
