"use strict";

const $ = (sel) => document.querySelector(sel);

async function api(path, opts) {
  const res = await fetch(path, opts);
  if (res.status === 204) return null;
  const text = await res.text();
  let data = text;
  try { data = text ? JSON.parse(text) : null; } catch (_) {}
  if (!res.ok) throw new Error((data && data.error) || res.statusText);
  return data;
}

function el(tag, attrs, ...children) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (k === "text") n.textContent = v;
    else if (k === "html") n.innerHTML = v;
    else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
    else n.setAttribute(k, v);
  }
  for (const c of children) n.append(c);
  return n;
}

const tabButtons = [...document.querySelectorAll(".tab")];
for (const btn of tabButtons) {
  btn.addEventListener("click", () => {
    tabButtons.forEach((b) => b.classList.toggle("active", b === btn));
    document.querySelectorAll(".panel").forEach((p) =>
      p.classList.toggle("active", p.id === "tab-" + btn.dataset.tab));
  });
}

function fmtSize(n) {
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(1) + " GB";
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + " MB";
  if (n >= 1 << 10) return (n / (1 << 10)).toFixed(0) + " KB";
  return n + " B";
}
function fmtTime(iso) {
  const d = new Date(iso);
  return d.toLocaleString();
}

// ---------------- status ----------------

async function renderStatus() {
  const s = await api("/api/status");
  $("#live-link").href = "http://" + location.hostname + ":" + s.live_port;

  const box = $("#tab-status");
  box.replaceChildren();

  const g = s.go2rtc;
  const camRows = s.cameras.map((c) => el("tr",
    el("td", { text: c.name }),
    el("td", { class: c.active ? "up" : "down", text: c.active ? "running" : "stopped" }),
    el("td", { text: c.mic ? "on" : "off" }),
    el("td", { text: c.mode })));

  box.append(
    el("div", { class: "card" },
      el("h2", { text: "Service" }),
      el("table", {},
        el("tr", {}, el("td", { text: "Version" }), el("td", { text: s.version })),
        el("tr", {}, el("td", { text: "Uptime" }), el("td", { text: (s.uptime_ns / 1e9).toFixed(0) + " s" })),
        el("tr", {}, el("td", { text: "Config" }), el("td", { class: "mono", text: s.config_path })),
        el("tr", {}, el("td", { text: "Record mode" }), el("td", { text: s.mode })),
        el("tr", {}, el("td", { text: "go2rtc" }),
          el("td", {}, el("span", { class: g.running ? "up" : "down", text: g.running ? "running" : "stopped" }),
            g.running ? " (pid " + g.pid + ")" : "")))),
    el("div", { class: "card" },
      el("h2", { text: "Cameras" }),
      el("table", {},
        el("tr", {}, el("th", { text: "Name" }), el("th", { text: "Runner" }), el("th", { text: "Mic" }), el("th", { text: "Mode" })),
        ...camRows)));
}

// ---------------- recordings ----------------

async function renderRecordings() {
  const list = await api("/api/recordings");
  const box = $("#rec-list");
  box.replaceChildren();

  if (!list.length) {
    box.append(el("div", { class: "card", text: "No recordings yet." }));
    return;
  }

  const byCam = new Map();
  for (const rec of list) {
    if (!byCam.has(rec.camera)) byCam.set(rec.camera, []);
    byCam.get(rec.camera).push(rec);
  }

  for (const [cam, recs] of byCam) {
    box.append(el("div", { class: "card" },
      el("h2", { text: cam + " (" + recs.length + ")" }),
      el("table", {},
        el("tr", {}, el("th", { text: "File" }), el("th", { text: "Recorded" }), el("th", { text: "Size" })),
        ...recs.map((rec) => el("tr", {},
          el("td", {}, el("a", {
            class: "play",
            onclick: () => play(rec),
          }, { text: rec.name })),
          el("td", { text: fmtTime(rec.mod_time) }),
          el("td", { text: fmtSize(rec.size) })))));
  }
}

function play(rec) {
  const player = $("#player");
  const src = "/api/recordings/" + encodeURIComponent(rec.camera) + "/" +
    encodeURIComponent(rec.name);
  if (player.dataset.src === src) { player.hidden = !player.hidden; return; }
  player.dataset.src = src;
  player.src = src;
  player.hidden = false;
  player.play().catch(() => {});
}

// ---------------- config ----------------

async function loadConfig() {
  $("#cfg-msg").textContent = "";
  const res = await fetch("/api/config");
  $("#cfg-text").value = await res.text();
}

$("#cfg-save").addEventListener("click", async () => {
  try {
    await api("/api/config", {
      method: "PUT",
      headers: { "Content-Type": "application/jsonc" },
      body: $("#cfg-text").value,
    });
    $("#cfg-msg").textContent = "saved (reloads on next tick)";
    $("#cfg-msg").className = "up";
  } catch (e) {
    $("#cfg-msg").textContent = e.message;
    $("#cfg-msg").className = "err";
  }
});
$("#cfg-reload").addEventListener("click", loadConfig);

// ---------------- boot ----------------

async function refresh() {
  const tab = document.querySelector(".tab.active").dataset.tab;
  try {
    if (tab === "status") await renderStatus();
    if (tab === "recordings") await renderRecordings();
  } catch (e) {
    document.querySelector(".panel.active").innerHTML =
      '<div class="card err">' + e.message + "</div>";
  }
}
loadConfig();
setInterval(refresh, 5000);
refresh();
