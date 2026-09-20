// codespace-status web UI: no framework, no build step, one polling loop.
"use strict";

// The VS Code webview injects its own transport; the browser uses HTTP.
const T = window.CS_TRANSPORT || {
  status: () => getJSON("/api/status"),
  select: (name) => postJSON("/api/select?name=" + encodeURIComponent(name)),
  deselect: () => postJSON("/api/deselect"),
  sync: () => postJSON("/api/sync"),
  setPoll: (ms) => postJSON("/api/poll?ms=" + ms),
  setTheme: (value) => postJSON("/api/theme?value=" + encodeURIComponent(value)),
  permissions: () => getJSON("/api/permissions"),
  auth: () => postJSON("/api/auth"),
};

function getJSON(path) {
  return fetch(path, { cache: "no-store" }).then((r) => r.json());
}

function postJSON(path) {
  return fetch(path, { method: "POST", cache: "no-store" }).then((r) => r.json());
}

const MB = 1024 * 1024;

function bytes(n) {
  n = Number(n) || 0;
  if (n < 1024) return n.toFixed(0) + " B";
  if (n < MB) return (n / 1024).toFixed(1) + " KB";
  if (n < 1024 * MB) return (n / MB).toFixed(1) + " MB";
  return (n / 1024 / MB).toFixed(2) + " GB";
}

function rate(n) { return bytes(n) + "/s"; }
function pct(n) { return (Number(n) || 0).toFixed(1) + "%"; }

// Seven fixed metrics, identical on every card.
const METRICS = [
  { key: "mem", label: "Memory", pick: (s) => s.mem_percent, max: 100,
    text: (s) => pct(s.mem_percent) + "  " + bytes(s.mem_used_bytes) + " / " + bytes(s.mem_total_bytes) },
  { key: "cpu", label: "CPU", pick: (s) => s.cpu_percent, max: 100, text: (s) => pct(s.cpu_percent) },
  { key: "net_in", label: "Net In (60s)", pick: (s) => s.net_in_bps, text: (s) => rate(s.net_in_bps) },
  { key: "net_out", label: "Net Out (60s)", pick: (s) => s.net_out_bps, text: (s) => rate(s.net_out_bps) },
  { key: "disk_io", label: "Disk IO", pick: (s) => s.disk_iops, text: (s) => (Number(s.disk_iops) || 0).toFixed(1) + " ops/s" },
  { key: "disk_write", label: "Disk Write", pick: (s) => s.disk_write_bps, text: (s) => rate(s.disk_write_bps) },
  { key: "disk_read", label: "Disk Read", pick: (s) => s.disk_read_bps, text: (s) => rate(s.disk_read_bps) },
];

const tabsEl = document.getElementById("tabs");
const cardsEl = document.getElementById("cards");
const selectedEl = document.getElementById("selected");
const noticeEl = document.getElementById("notice");
const pollEl = document.getElementById("poll");
const themeEl = document.getElementById("theme");

const cards = new Map();
let timer = null;
let pollMS = 2000;
let busy = false;

function css(name) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || "#3a6ea5";
}

// Bar chart of the rolling window, scaled to the metric maximum.
function draw(canvas, values, fixedMax) {
  const dpr = window.devicePixelRatio || 1;
  const w = Math.max(1, Math.round(canvas.clientWidth * dpr));
  const h = Math.max(1, Math.round(canvas.clientHeight * dpr));
  if (canvas.width !== w || canvas.height !== h) {
    canvas.width = w;
    canvas.height = h;
  }
  const ctx = canvas.getContext("2d");
  ctx.clearRect(0, 0, w, h);
  if (!values.length) return;
  const slots = 60;
  let max = fixedMax || 1;
  if (!fixedMax) { for (const v of values) max = Math.max(max, v); }
  const bw = w / slots;
  const offset = slots - values.length;
  ctx.fillStyle = css("--accent");
  values.forEach((v, i) => {
    const bh = Math.max(1, (Math.min(v, max) / max) * (h - 2));
    ctx.fillRect((offset + i) * bw, h - bh, Math.max(1, bw - dpr), bh);
  });
}

function makeCard(cs) {
  const card = document.createElement("section");
  card.className = "card";

  const head = document.createElement("div");
  head.className = "card-head";
  const dot = document.createElement("span");
  const name = document.createElement("span");
  name.className = "name";
  const meta = document.createElement("span");
  meta.className = "muted";
  const badge = document.createElement("span");
  badge.className = "badge";
  head.append(dot, name, meta, badge);

  const error = document.createElement("div");
  error.className = "error";
  error.hidden = true;

  const metrics = document.createElement("div");
  metrics.className = "metrics";
  const cells = {};
  for (const m of METRICS) {
    const cell = document.createElement("div");
    cell.className = "metric";
    const label = document.createElement("div");
    label.className = "metric-label";
    label.textContent = m.label;
    const value = document.createElement("div");
    value.className = "metric-value";
    value.textContent = "-";
    const canvas = document.createElement("canvas");
    cell.append(label, value, canvas);
    metrics.appendChild(cell);
    cells[m.key] = { value: value, canvas: canvas };
  }

  card.append(head, error, metrics);
  const entry = { el: card, dot: dot, name: name, meta: meta, badge: badge, error: error, cells: cells };
  cards.set(cs.name, entry);
  return entry;
}

function updateCard(cs) {
  const c = cards.get(cs.name) || makeCard(cs);
  c.el.classList.toggle("selected", !!cs.selected);
  c.dot.className = "dot " + cs.status;
  c.name.textContent = cs.display_name || cs.name;
  const bits = [cs.name];
  if (cs.repository) bits.push(cs.repository);
  if (cs.machine) bits.push(cs.machine);
  bits.push(cs.state || "unknown");
  c.meta.textContent = bits.join("  ·  ");
  c.badge.textContent = cs.selected ? "active · " + cs.status : cs.status;

  const samples = cs.samples || [];
  const latest = cs.latest;
  c.error.hidden = !cs.error;
  if (cs.error) c.error.textContent = cs.error;

  for (const m of METRICS) {
    const cell = c.cells[m.key];
    cell.value.textContent = latest ? m.text(latest) : (cs.status === "active" ? "sampling..." : "-");
    draw(cell.canvas, samples.map(m.pick).map((v) => Number(v) || 0), m.max);
  }
  return c;
}

function renderTabs(list, selected) {
  tabsEl.replaceChildren();
  for (const cs of list) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "tab" + (cs.selected ? " active" : "");
    const dot = document.createElement("span");
    dot.className = "dot " + cs.status;
    const label = document.createElement("span");
    label.textContent = cs.display_name || cs.name;
    b.append(dot, label);
    b.onclick = () => act(cs.selected ? T.deselect() : T.select(cs.name));
    tabsEl.appendChild(b);
  }
  if (!list.length) {
    const empty = document.createElement("span");
    empty.className = "muted";
    empty.textContent = "no codespaces found - press Sync";
    tabsEl.appendChild(empty);
  }
  selectedEl.textContent = selected ? "active: " + selected : "no codespace selected";
}

function render(status) {
  const list = status.codespaces || [];
  renderTabs(list, status.selected);

  const keep = new Set(list.map((cs) => cs.name));
  for (const [name, c] of cards) {
    if (!keep.has(name)) { c.el.remove(); cards.delete(name); }
  }
  list.forEach((cs, i) => {
    const c = updateCard(cs);
    if (cardsEl.children[i] !== c.el) cardsEl.insertBefore(c.el, cardsEl.children[i] || null);
  });

  if (status.poll_ms && status.poll_ms !== pollMS) { pollMS = status.poll_ms; applyPoll(); }
  if (pollEl.value !== String(pollMS)) pollEl.value = String(pollMS);
  if (status.sync_error) notice("sync: " + status.sync_error);
  if (status.theme && !window.CS_VSCODE) applyTheme(status.theme, false);
}

function notice(text) {
  noticeEl.textContent = text;
  noticeEl.hidden = !text;
}

async function refresh() {
  if (busy) return;
  busy = true;
  try {
    render(await T.status());
  } catch (e) {
    selectedEl.textContent = "disconnected";
  } finally {
    busy = false;
  }
}

function applyPoll() {
  if (timer) clearInterval(timer);
  timer = setInterval(refresh, Math.max(250, pollMS));
}

function applyTheme(theme, persist) {
  document.documentElement.setAttribute("data-theme", theme);
  themeEl.textContent = theme === "dark" ? "Light" : "Dark";
  if (persist) T.setTheme(theme);
}

async function act(promise) {
  try {
    const res = await promise;
    if (res && res.error) notice(res.error); else notice("");
  } catch (e) {
    notice(String(e));
  }
  refresh();
}

pollEl.onchange = () => {
  pollMS = Number(pollEl.value);
  applyPoll();
  act(T.setPoll(pollMS));
};

themeEl.onclick = () => {
  const next = document.documentElement.getAttribute("data-theme") === "dark" ? "light" : "dark";
  applyTheme(next, true);
};

document.getElementById("sync").onclick = () => { notice("syncing..."); act(T.sync()); };

document.getElementById("perms").onclick = async () => {
  notice("checking permissions...");
  try {
    const p = await T.permissions();
    const lines = [
      "gh installed: " + p.gh_installed,
      "authenticated: " + p.authenticated + (p.account ? " (" + p.account + ")" : ""),
      "token scopes: " + ((p.scopes || []).join(", ") || "unknown"),
      "codespace access: " + (p.can_list_codespaces ? "yes" : "no"),
    ].concat(p.messages || []);
    notice(lines.join("\n"));
  } catch (e) {
    notice(String(e));
  }
};

document.getElementById("auth").onclick = async () => {
  notice("starting authentication...");
  try {
    const r = await T.auth();
    notice([r.output, r.error ? "error: " + r.error : "", "if this stalls, run: " + r.command]
      .filter(Boolean).join("\n"));
  } catch (e) {
    notice(String(e));
  }
};

applyTheme(document.documentElement.getAttribute("data-theme") || "light", false);
refresh();
applyPoll();
