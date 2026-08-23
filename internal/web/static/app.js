const $ = (sel) => document.querySelector(sel);

async function api(path, opts) {
  const res = await fetch(path, opts);
  if (res.status === 204) return null;
  const text = await res.text();
  let data = text;
  try {
    data = text ? JSON.parse(text) : null;
  } catch (_) {}
  if (!res.ok) throw new Error(data?.error || res.statusText);
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
  for (const c of children) {
    if (typeof c === "string") n.appendChild(document.createTextNode(c));
    else if (c) n.appendChild(c);
  }
  return n;
}

const tabButtons = [...document.querySelectorAll(".tab")];
function activateTab(tab) {
  const btn = tabButtons.find((b) => b.dataset.tab === tab);
  if (!btn) return false;
  for (const b of tabButtons) {
    b.classList.toggle("active", b === btn);
    b.setAttribute("aria-selected", b === btn ? "true" : "false");
  }
  for (const p of document.querySelectorAll(".panel")) {
    p.classList.toggle("active", p.id === `tab-${btn.dataset.tab}`);
  }
  return true;
}
for (const btn of tabButtons) {
  btn.addEventListener("click", () => {
    activateTab(btn.dataset.tab);
    history.replaceState(null, "", `#/${btn.dataset.tab}`);
    if (btn.dataset.tab === "recordings") renderRecordings();
    else if (btn.dataset.tab === "logs") renderLogs();
    else refresh();
  });
}
if (location.hash.startsWith("#/")) activateTab(location.hash.slice(2));

function fmtSize(n) {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GB`;
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(0)} KB`;
  return `${n} B`;
}
function fmtTime(iso) {
  return new Date(iso).toLocaleString();
}
function esc(s) {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

let toastContainer = null;
function toast(msg, kind = "info") {
  if (!toastContainer) toastContainer = $("#toast-container");
  const t = el("div", { class: `toast ${kind}`, text: msg });
  toastContainer.appendChild(t);
  setTimeout(
    () => {
      t.classList.add("leaving");
      setTimeout(() => t.remove(), 200);
    },
    kind === "error" ? 5000 : 3000,
  );
}

function withButtonLoading(btn, fn) {
  return async (...args) => {
    if (btn.disabled) return;
    btn.disabled = true;
    const orig = btn.textContent;
    btn.textContent = "\u2026";
    try {
      await fn(...args);
    } finally {
      btn.disabled = false;
      btn.textContent = orig;
    }
  };
}

function _openModal(buildFn) {
  const overlay = el("div", { class: "modal-overlay" });
  const modal = el("div", {
    class: "modal",
    role: "dialog",
    "aria-modal": "true",
  });
  overlay.appendChild(modal);
  document.body.appendChild(overlay);
  const prevFocus = document.activeElement;
  function close() {
    overlay.remove();
    if (prevFocus?.focus) prevFocus.focus();
  }
  overlay.addEventListener("click", (e) => {
    if (e.target === overlay) close();
  });
  overlay.addEventListener("keydown", (e) => {
    if (e.key === "Tab") {
      const focusable = modal.querySelectorAll(
        'button, [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
      );
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (e.shiftKey) {
        if (document.activeElement === first) {
          e.preventDefault();
          last.focus();
        }
      } else {
        if (document.activeElement === last) {
          e.preventDefault();
          first.focus();
        }
      }
    }
  });
  buildFn(modal, close);
  const firstFocusable = modal.querySelector(
    'button, input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])',
  );
  if (firstFocusable) firstFocusable.focus();
  return { overlay, modal, close };
}

// ---------------- devices cache ----------------

let deviceCache = null;
async function getDevices() {
  if (!deviceCache) {
    try {
      deviceCache = await api("/api/devices");
    } catch (_) {
      deviceCache = { video: [], audio: [], encoders: [] };
      toast("Could not enumerate devices", "error");
    }
  }
  return deviceCache;
}
// ---------------- form helpers ----------------

const RESOLUTION_PRESETS = [
  { label: "640\u00d7480 (VGA)", w: 640, h: 480 },
  { label: "1280\u00d7720 (HD)", w: 1280, h: 720 },
  { label: "1920\u00d71080 (FHD)", w: 1920, h: 1080 },
];

const deviceFormatsCache = {};
async function fetchDeviceFormats(deviceName) {
  if (!deviceName) return [];
  if (deviceFormatsCache[deviceName]) return deviceFormatsCache[deviceName];
  try {
    const formats = await api(
      `/api/devices/${encodeURIComponent(deviceName)}/formats`,
    );
    deviceFormatsCache[deviceName] = formats;
    return formats;
  } catch (_) {
    return [];
  }
}

function resolutionSelect(w, h, wName, hName, formats) {
  const presets =
    formats && formats.length > 0
      ? formats.map((f) => ({
          label: `${f.width}\u00d7${f.height} @ ${f.min_fps === f.max_fps ? f.max_fps : `${f.min_fps}\u2013${f.max_fps}`} fps`,
          w: f.width,
          h: f.height,
        }))
      : RESOLUTION_PRESETS;
  const s = el("select", {
    class: "res-select",
    "data-name": `${wName}.__preset`,
  });
  const match = presets.find((r) => r.w === w && r.h === h);
  s.appendChild(el("option", { value: "", text: match ? "" : "Custom" }));
  for (const r of presets) {
    const opt = el("option", { value: `${r.w}x${r.h}`, text: r.label });
    if (r.w === w && r.h === h) opt.selected = true;
    s.appendChild(opt);
  }
  s.addEventListener("change", () => {
    const parts = s.value.split("x");
    if (parts.length === 2) {
      const form = s.closest("#cfg-form");
      const wInput = form?.querySelector(`[data-name="${wName}"]`);
      const hInput = form?.querySelector(`[data-name="${hName}"]`);
      if (wInput) wInput.value = parts[0];
      if (hInput) hInput.value = parts[1];
      wInput?.dispatchEvent(new Event("change", { bubbles: true }));
      hInput?.dispatchEvent(new Event("change", { bubbles: true }));
    }
  });
  return s;
}

const TOOLTIPS = {
  "record.mode":
    "How recordings are triggered: on motion/sound events, continuously, or combined.",
  "record.retain_hours":
    "Delete recordings older than this (0 = keep forever).",
  "record.retain_mb":
    "Delete oldest recordings when total exceeds this (0 = no limit).",
  "paths.record_dir": "Root directory for camera recordings.",
  "paths.log_dir": "Directory for service log files.",
  "paths.rtsp_host": "Host IP used in go2rtc RTSP stream URLs.",
  "web.bind": "IP address the web panel listens on (127.0.0.1 = local only).",
  "web.port": "Port for the web panel.",
  "go2rtc.api_port": "Port for go2rtc's internal API.",
  "go2rtc.rtsp_port": "Port for go2rtc's RTSP server.",
  "go2rtc.webrtc_port": "Port for go2rtc's WebRTC server.",
  "gpu.encoders":
    "Available encoders on this system. 'auto' picks the best one.",
  "log.level": "Minimum log verbosity (debug is very noisy).",
  "log.max_mb": "Max size per log file before rotation.",
  "log.keep": "Number of rotated log files to keep.",
  "monitor.enabled": "Auto-check service health and send alerts.",
  "monitor.interval_sec": "Seconds between health checks.",
  "monitor.alert_after_sec": "Seconds of downtime before sending an alert.",
  "monitor.ntfy_topic": "ntfy.sh topic for push notifications.",
  detection:
    "Motion input: rawvideo (fast, needs go2rtc stream) or jpeg (MJPEG, works with any source).",
  "motion.enabled": "Enable motion detection for this camera.",
  "motion.width": "Downscale width for motion analysis (lower = faster).",
  "motion.height": "Downscale height for motion analysis.",
  "motion.fps": "Frame rate for motion analysis.",
  "motion.threshold": "Pixel-change sensitivity (lower = more sensitive).",
  "motion.min_pixels": "Minimum changed pixels to trigger motion.",
  "motion.bg_alpha": "Background adaptation speed (lower = slower adaptation).",
  "motion.post_roll_sec": "Continue recording after motion stops.",
  "motion.event_cap_sec": "Max recording length per motion event.",
  "motion.restart_backoff_sec": "Delay between motion detector restarts.",
  "record.enabled": "Enable recording for this camera.",
  "record.prefix": "Filename prefix for recording segments.",
  "record.out_dir": "Camera-specific recording directory (overrides global).",
  "record.video.codec":
    "NVENC = GPU (fast), libx264 = CPU, copy = passthrough.",
  "record.audio.codec": "AAC = standard, copy = passthrough (no re-encode).",
  "record.audio.bitrate": "Audio bitrate (e.g. 64k, 128k).",
  "record.pre_roll_sec": "Buffer pre-roll seconds from the ring buffer.",
  "record.segment_sec": "Ring buffer segment length in seconds.",
  priority: "Recording priority (higher = preferred when disk is full).",
  "record.video.preset":
    "NVENC: p1 (fastest) to p7 (best quality). libx264: ultrafast..veryslow.",
  "record.video.rc":
    "Rate control: vbr (variable, smaller files) or cbr (constant bandwidth).",
  "record.video.cq":
    "Constant quality (lower = better, 0 = auto). Typical: 20-35.",
  "record.video.maxrate": "Max bitrate cap (e.g. 4M, 8M). Prevents spikes.",
  "record.video.tune": "Encoder tuning: ll (low-latency), zerolatency, none.",
  "record.video.g":
    "GOP size: keyframe interval in frames. Lower = faster seek.",
  "record.video.bf": "B-frames between frames. 0 = none (low-latency).",
  "live.hls": "Enable HLS output for iPhone/Safari live view.",
  "live.video.codec": "Encoder for the live stream (separate from recording).",
  "live.video.preset": "NVENC: p1 (fastest) to p7 (best quality).",
  "live.video.rc": "Rate control: vbr or cbr.",
  "live.video.cq": "Constant quality level (lower = better).",
  "live.video.maxrate": "Max bitrate cap for live stream.",
  "live.video.tune": "Encoder tuning: ll (low-latency) recommended for live.",
  "live.video.g": "GOP size for live stream.",
  "live.audio.codec": "Audio codec for the live stream.",
  "mic.enabled": "Enable microphone capture for this camera.",
  "mic.gain": "Software gain multiplier (1.0 = no boost).",
  "mic.dc_block": "Remove DC offset (constant hum) from mic signal.",
  "mic.noise_gate": "Suppress audio below this RMS level.",
  "mic.sample_rate": "Capture sample rate (48000 = standard, 22050 = voice).",
  "mic.channels": "Number of audio channels (1 = mono, 2 = stereo).",
  "mic.backend":
    "Audio backend: WASAPI (default) or auto (WASAPI + WDM-KS fallback).",
  "mic.device_name": "Select the microphone to capture audio from.",
  "image.brightness": "Camera brightness adjustment (-1 to 1).",
  "image.contrast": "Camera contrast adjustment (-1 to 1).",
  "image.saturation": "Camera color saturation (-1 to 1).",
  "image.hue": "Camera hue rotation (-1 to 1).",
  "image.gamma": "Camera gamma correction (-1 to 1).",
  "sound.enabled": "Enable sound event detection.",
  "sound.abs_floor": "Minimum absolute RMS to consider as sound.",
  "sound.ratio": "Ratio above floor to trigger sound event.",
  "sound.tau": "Sound envelope time constant.",
  "sound.hold_blocks": "How long to hold sound detection after trigger.",
  "sound.drop_blocks":
    "How long to suppress sound after it drops below threshold.",
  name: "Camera name (used in filenames and stream URLs).",
  source: "DirectShow for USB cameras, RTSP for network cameras.",
  device_name: "Video capture device on this machine.",
  width: "Capture width in pixels.",
  height: "Capture height in pixels.",
  fps: "Capture frame rate.",
  rtsp_url: "RTSP stream URL (e.g. rtsp://192.168.1.10:554/stream).",
};

let fieldIdCounter = 0;
function field(label, input, cls) {
  const row = el("div", { class: `field${cls ? ` ${cls}` : ""}` });
  const id = `field-${++fieldIdCounter}`;
  const lbl = el("label", { text: label, for: id });
  const name = input?.getAttribute?.("data-name");
  const tip = name && TOOLTIPS[name];
  if (tip) {
    const icon = el("span", { class: "field-tip", text: "?", "data-tip": tip });
    lbl.appendChild(icon);
  }
  row.appendChild(lbl);
  if (input?.setAttribute) input.setAttribute("id", id);
  row.appendChild(input);
  return row;
}

function textInput(val, name, opts) {
  const i = el("input", { type: "text", value: val ?? "", "data-name": name });
  if (opts?.size) i.style.width = opts.size;
  return i;
}

function numInput(val, name, opts) {
  const i = el("input", { type: "number", value: val ?? 0, "data-name": name });
  if (opts?.min !== undefined) i.min = opts.min;
  if (opts?.max !== undefined) i.max = opts.max;
  if (opts?.step !== undefined) i.step = opts.step;
  if (opts?.size) i.style.width = opts.size;
  return i;
}

function chkInput(val, name) {
  return el("input", {
    type: "checkbox",
    "data-name": name,
    ...(val ? { checked: "checked" } : {}),
  });
}

function selectInput(val, name, opts) {
  const s = el("select", { "data-name": name });
  let matched = false;
  for (const o of opts) {
    const opt = el("option", { value: o.value, text: o.label });
    if (o.value === val) {
      opt.selected = true;
      matched = true;
    }
    s.appendChild(opt);
  }
  if (!matched && val) {
    const opt = el("option", { value: val, text: val });
    opt.selected = true;
    s.appendChild(opt);
  }
  return s;
}

function pathInput(val, name) {
  const wrap = el("div", { class: "path-input-wrap" });
  const inp = el("input", {
    type: "text",
    value: val ?? "",
    "data-name": name,
  });
  inp.style.flex = "1";
  const btn = el("button", {
    class: "secondary path-browse-btn",
    text: "...",
    type: "button",
  });
  btn.addEventListener("click", () => browseFolder(inp));
  wrap.appendChild(inp);
  wrap.appendChild(btn);
  return wrap;
}

function browseFolder(targetInput) {
  const startPath = targetInput.value || "";
  const overlay = el("div", { class: "modal-overlay" });
  const modal = el("div", { class: "modal", style: "max-width:500px" });
  modal.appendChild(el("h2", { text: "Select folder" }));

  const list = el("div", { class: "zone-list browse-list" });
  const selectBtn = el("button", {
    text: "Select this folder",
    style: "margin-top:8px;width:100%",
  });
  selectBtn.disabled = true;

  let selectedPath = "";

  async function loadDir(dirPath) {
    list.replaceChildren();
    selectBtn.disabled = true;
    selectedPath = "";
    try {
      const data = await api(`/api/browse?path=${encodeURIComponent(dirPath)}`);
      selectedPath = data.path || "";

      const pathRow = el("div", { class: "browse-path" });
      // Root link to go back to drive selection.
      const rootLink = el("a", { text: "\u2302", href: "#" });
      rootLink.addEventListener("click", (e) => {
        e.preventDefault();
        loadDir("");
      });
      pathRow.appendChild(rootLink);
      const crumbs = (data.path || "").split(/[/\\]/).filter(Boolean);
      let accumulated = "";
      const sep = data.path?.includes("\\") ? "\\" : "/";
      for (let i = 0; i < crumbs.length; i++) {
        accumulated += (i === 0 ? "" : sep) + crumbs[i];
        const seg = accumulated;
        if (i > 0)
          pathRow.appendChild(
            el("span", { text: "\u203a", style: "color:var(--muted)" }),
          );
        const link = el("a", { text: crumbs[i], href: "#" });
        link.addEventListener("click", (e) => {
          e.preventDefault();
          loadDir(seg);
        });
        pathRow.appendChild(link);
      }
      if (crumbs.length === 0)
        pathRow.appendChild(
          el("span", { text: "(drives)", style: "color:var(--muted)" }),
        );
      const heading = modal.querySelector(".browse-path");
      if (heading) heading.replaceWith(pathRow);
      else modal.insertBefore(pathRow, list);

      if (data.path) selectBtn.disabled = false;
      if (data.dirs) {
        for (const d of data.dirs) {
          const row = el("div", {
            class: "zone-list-item",
            style: "cursor:pointer",
          });
          row.appendChild(
            el("span", { text: d.name === ".." ? "\u2191 .." : `${d.name}/` }),
          );
          row.addEventListener("click", () => loadDir(d.path));
          list.appendChild(row);
        }
      }
      if (!data.dirs?.length) {
        list.appendChild(
          el("div", {
            class: "zone-list-item",
            text: "(empty - no subfolders)",
            style: "color:var(--muted)",
          }),
        );
      }
    } catch (e) {
      list.appendChild(
        el("div", { class: "zone-list-item err", text: `Error: ${e.message}` }),
      );
    }
  }

  async function createFolder() {
    const dirPath = selectedPath || startPath;
    if (!dirPath) return;
    const name = prompt("New folder name:");
    if (!name) return;
    const fullPath = dirPath + (dirPath.includes("\\") ? "\\" : "/") + name;
    try {
      await api(`/api/mkdir?path=${encodeURIComponent(fullPath)}`, {
        method: "POST",
      });
      await loadDir(dirPath);
    } catch (e) {
      list.appendChild(
        el("div", { class: "zone-list-item err", text: `Error: ${e.message}` }),
      );
    }
  }

  selectBtn.addEventListener("click", () => {
    targetInput.value = selectedPath;
    targetInput.dispatchEvent(new Event("change", { bubbles: true }));
    overlay.remove();
  });

  const btnRow = el("div", { class: "row", style: "margin-top:8px" });
  btnRow.appendChild(selectBtn);
  btnRow.appendChild(
    el("button", {
      class: "secondary",
      text: "New folder",
      onclick: createFolder,
    }),
  );
  btnRow.appendChild(
    el("button", {
      class: "secondary",
      text: "Cancel",
      onclick: () => overlay.remove(),
    }),
  );
  modal.appendChild(list);
  modal.appendChild(btnRow);
  overlay.appendChild(modal);
  overlay.addEventListener("click", (e) => {
    if (e.target === overlay) overlay.remove();
  });
  document.body.appendChild(overlay);
  loadDir(startPath);
}

function section(title, ...kids) {
  const d = el("details", { class: "cfg-section" });
  d.appendChild(el("summary", { text: title }));
  const body = el("div", { class: "cfg-section-body" });
  for (const k of kids) if (k) body.appendChild(k);
  d.appendChild(body);
  return d;
}

function subsection(title, ...kids) {
  const d = el("details", { class: "cfg-subsection" });
  d.appendChild(el("summary", { text: title }));
  const body = el("div", { class: "cfg-subsection-body" });
  for (const k of kids) if (k) body.appendChild(k);
  d.appendChild(body);
  return d;
}

function grid(...fields) {
  const g = el("div", { class: "field-grid" });
  for (const f of fields) if (f) g.appendChild(f);
  return g;
}

// ---------------- config form ----------------

let currentConfig = null;
let savedSnapshot = null;
let advancedMode = false;
let devices = null;

function saveAccordionState() {
  const open = new Set();
  for (const d of document.querySelectorAll("#cfg-form details[open]")) {
    const s = d.querySelector("summary");
    if (s) open.add(s.textContent.trim());
  }
  return open;
}

function restoreAccordionState(open) {
  if (!open) return;
  for (const d of document.querySelectorAll("#cfg-form details")) {
    const s = d.querySelector("summary");
    if (s && open.has(s.textContent.trim())) d.open = true;
  }
}

function renderConfigForm(cfg) {
  currentConfig = cfg;
  const wrap = $("#cfg-form");
  const wasOpen = saveAccordionState();
  wrap.replaceChildren();

  const globalSec = section(
    "Global Settings",
    subsection(
      "Recording",
      grid(
        field(
          "Mode",
          selectInput(cfg.record?.mode ?? "event", "record.mode", [
            { value: "event", label: "Event (motion/sound)" },
            { value: "continuous", label: "Continuous" },
            { value: "combined", label: "Combined (tiled)" },
            { value: "none", label: "None (live only)" },
          ]),
        ),
        field(
          "Retain hours",
          numInput(cfg.record?.retain_hours ?? 72, "record.retain_hours", {
            min: 0,
            max: 8760,
            size: "80px",
          }),
        ),
        field(
          "Retain MB",
          numInput(cfg.record?.retain_mb ?? 0, "record.retain_mb", {
            min: 0,
            size: "80px",
          }),
        ),
      ),
    ),
    subsection(
      "Paths",
      grid(
        field(
          "Record dir",
          pathInput(cfg.paths?.record_dir ?? "", "paths.record_dir"),
        ),
        field("Log dir", pathInput(cfg.paths?.log_dir ?? "", "paths.log_dir")),
        field(
          "RTSP host",
          textInput(cfg.paths?.rtsp_host ?? "127.0.0.1", "paths.rtsp_host"),
        ),
      ),
    ),
    subsection(
      "Network",
      grid(
        field(
          "Panel bind",
          textInput(cfg.web?.bind ?? "127.0.0.1", "web.bind"),
        ),
        field(
          "Panel port",
          numInput(cfg.web?.port ?? 8080, "web.port", {
            min: 1,
            max: 65535,
            size: "80px",
          }),
        ),
        field(
          "go2rtc API",
          numInput(cfg.go2rtc?.api_port ?? 1984, "go2rtc.api_port", {
            min: 1,
            max: 65535,
            size: "80px",
          }),
        ),
        field(
          "RTSP port",
          numInput(cfg.go2rtc?.rtsp_port ?? 8554, "go2rtc.rtsp_port", {
            min: 1,
            max: 65535,
            size: "80px",
          }),
        ),
        field(
          "WebRTC port",
          numInput(cfg.go2rtc?.webrtc_port ?? 8555, "go2rtc.webrtc_port", {
            min: 1,
            max: 65535,
            size: "80px",
          }),
        ),
      ),
    ),
  );
  if (advancedMode) {
    const globalBody = globalSec.querySelector(".cfg-section-body");
    const encoderOpts = [
      { value: "auto", label: "Auto — pick best available" },
    ];
    if (devices?.encoders) {
      const gpu = devices.encoders.filter((e) => e.name.includes("_nvenc"));
      const cpu = devices.encoders.filter((e) => !e.name.includes("_nvenc"));
      if (gpu.length)
        for (const e of gpu)
          encoderOpts.push({ value: e.name, label: `${e.name} — ${e.info}` });
      if (cpu.length)
        for (const e of cpu)
          encoderOpts.push({ value: e.name, label: `${e.name} — ${e.info}` });
    }
    globalBody.appendChild(
      subsection(
        "Encoders",
        grid(
          field(
            "GPU encoders",
            selectInput(
              cfg.gpu?.encoders ?? "auto",
              "gpu.encoders",
              encoderOpts,
            ),
          ),
        ),
      ),
    );
    globalBody.appendChild(
      subsection(
        "Logging",
        grid(
          field(
            "Level",
            selectInput(cfg.log?.level ?? "info", "log.level", [
              { value: "debug", label: "Debug" },
              { value: "info", label: "Info" },
              { value: "warn", label: "Warn" },
            ]),
          ),
          field(
            "Max MB",
            numInput(cfg.log?.max_mb ?? 10, "log.max_mb", {
              min: 1,
              size: "80px",
            }),
          ),
          field(
            "Keep",
            numInput(cfg.log?.keep ?? 5, "log.keep", { min: 1, size: "80px" }),
          ),
        ),
      ),
    );
    globalBody.appendChild(
      subsection(
        "Monitor",
        grid(
          field(
            "Enabled",
            chkInput(cfg.monitor?.enabled ?? true, "monitor.enabled"),
          ),
          field(
            "Interval (s)",
            numInput(cfg.monitor?.interval_sec ?? 10, "monitor.interval_sec", {
              min: 1,
              size: "80px",
            }),
          ),
          field(
            "Alert after (s)",
            numInput(
              cfg.monitor?.alert_after_sec ?? 300,
              "monitor.alert_after_sec",
              { min: 0, size: "80px" },
            ),
          ),
          field(
            "ntfy topic",
            textInput(cfg.monitor?.ntfy_topic ?? "", "monitor.ntfy_topic"),
          ),
        ),
      ),
    );
  }
  wrap.appendChild(globalSec);

  const cams = cfg.cameras || [];
  for (let i = 0; i < cams.length; i++) {
    wrap.appendChild(renderCameraForm(cams[i], i));
  }

  const addBtn = el("button", {
    class: "secondary",
    text: "+ Add camera",
    onclick: showAddCameraModal,
  });
  wrap.appendChild(addBtn);

  restoreAccordionState(wasOpen);
  updateUnsavedDot();
  updateDeviceFormats();
}

async function updateDeviceFormats() {
  if (!currentConfig?.cameras) return;
  for (let i = 0; i < currentConfig.cameras.length; i++) {
    const cam = currentConfig.cameras[i];
    if (!cam.device_name || cam.source !== "dshow") continue;
    const formats = await fetchDeviceFormats(cam.device_name);
    if (!formats || formats.length === 0) continue;
    const form = $("#cfg-form");
    const wName = `cameras.${i}.width`;
    const hName = `cameras.${i}.height`;
    const oldSelect = form?.querySelector(`[data-name="${wName}.__preset"]`);
    if (!oldSelect) continue;
    const newSelect = resolutionSelect(
      cam.width ?? 1920,
      cam.height ?? 1080,
      wName,
      hName,
      formats,
    );
    oldSelect.replaceWith(newSelect);
  }
}

function codecOpts(devices) {
  const opts = [{ value: "copy", label: "Copy (passthrough, no re-encode)" }];
  if (devices?.encoders) {
    for (const e of devices.encoders) {
      opts.push({ value: e.name, label: `${e.name} — ${e.info}` });
    }
  } else if (devices?.nvenc) {
    opts.push(
      { value: "h264_nvenc", label: "H.264 NVENC — GPU, fast" },
      { value: "hevc_nvenc", label: "HEVC NVENC — GPU, smaller files" },
      { value: "av1_nvenc", label: "AV1 NVENC — GPU, best compression" },
    );
  }
  if (!devices?.nvenc) {
    opts.push({ value: "libx264", label: "H.264 (CPU)" });
  }
  return opts;
}

function renderCameraForm(cam, idx) {
  const p = (k) => `cameras.${idx}.${k}`;
  const prefix = cam.record?.prefix || cam.name || `cam${idx + 1}`;

  const camSec = el("details", { class: "cfg-camera" });
  camSec.appendChild(
    el("summary", {
      html: `<span class="cam-name">${esc(cam.name || "unnamed")}</span> <span class="cam-source">${esc(cam.source || "")}</span>`,
    }),
  );
  const body = el("div", { class: "cfg-camera-body" });

  body.appendChild(
    el("button", {
      class: "danger remove-cam",
      text: "Remove camera",
      onclick: () => {
        if (
          !confirm(
            `Remove camera "${cam.name || "unnamed"}"? You can Revert before saving.`,
          )
        )
          return;
        currentConfig.cameras.splice(idx, 1);
        renderConfigForm(currentConfig);
      },
    }),
  );

  const videoOpts = devices?.video?.length
    ? devices.video.map((d) => ({ value: d.name, label: d.name }))
    : [{ value: "", label: "(auto-detect)" }];

  body.appendChild(
    subsection(
      "Basic",
      grid(
        field("Name", textInput(cam.name ?? "", p("name"))),
        field(
          "Source",
          selectInput(cam.source ?? "dshow", p("source"), [
            { value: "dshow", label: "DirectShow (USB)" },
            { value: "rtsp", label: "RTSP" },
          ]),
        ),
        field(
          "Device",
          selectInput(cam.device_name ?? "", p("device_name"), videoOpts),
        ),
      ),
      grid(
        field(
          "Resolution",
          resolutionSelect(
            cam.width ?? 1920,
            cam.height ?? 1080,
            p("width"),
            p("height"),
          ),
        ),
        field(
          "FPS",
          numInput(cam.fps ?? 15, p("fps"), { min: 1, max: 60, size: "60px" }),
        ),
      ),
      grid(
        field(
          "Width",
          numInput(cam.width ?? 1920, p("width"), { min: 1, size: "80px" }),
        ),
        field(
          "Height",
          numInput(cam.height ?? 1080, p("height"), { min: 1, size: "80px" }),
        ),
      ),
      cam.source === "rtsp"
        ? grid(
            field(
              "RTSP URL",
              textInput(cam.rtsp_url ?? "", p("rtsp_url"), { size: "100%" }),
            ),
          )
        : null,
    ),
  );

  body.appendChild(
    subsection(
      "Recording",
      grid(
        field(
          "Enabled",
          chkInput(cam.record?.enabled ?? true, p("record.enabled")),
        ),
        field("Prefix", textInput(prefix, p("record.prefix"))),
        field(
          "Output dir",
          pathInput(cam.record?.out_dir ?? "", p("record.out_dir")),
        ),
      ),
      grid(
        field(
          "Video codec",
          selectInput(
            cam.record?.video?.codec ?? "h264_nvenc",
            p("record.video.codec"),
            codecOpts(devices),
          ),
        ),
        field(
          "Audio codec",
          selectInput(
            cam.record?.audio?.codec ?? "aac",
            p("record.audio.codec"),
            [
              { value: "aac", label: "AAC" },
              { value: "copy", label: "Copy" },
            ],
          ),
        ),
        field(
          "Audio bitrate",
          textInput(
            cam.record?.audio?.bitrate ?? "64k",
            p("record.audio.bitrate"),
            { size: "80px" },
          ),
        ),
      ),
      grid(
        field(
          "Pre-roll (s)",
          numInput(cam.record?.pre_roll_sec ?? 0, p("record.pre_roll_sec"), {
            min: 0,
            max: 60,
            size: "60px",
          }),
        ),
        field(
          "Priority",
          numInput(cam.priority ?? 5, p("priority"), {
            min: 0,
            max: 10,
            size: "60px",
          }),
        ),
      ),
    ),
  );

  if (advancedMode) {
    body.appendChild(
      subsection(
        "Video Encoder",
        grid(
          field(
            "Preset",
            textInput(
              cam.record?.video?.preset ?? "p2",
              p("record.video.preset"),
            ),
          ),
          field(
            "RC",
            textInput(cam.record?.video?.rc ?? "vbr", p("record.video.rc")),
          ),
          field(
            "CQ",
            numInput(cam.record?.video?.cq ?? 28, p("record.video.cq"), {
              min: 0,
              max: 51,
              size: "60px",
            }),
          ),
          field(
            "Max rate",
            textInput(
              cam.record?.video?.maxrate ?? "4M",
              p("record.video.maxrate"),
              { size: "80px" },
            ),
          ),
          field(
            "GOP",
            numInput(cam.record?.video?.g ?? 15, p("record.video.g"), {
              min: 1,
              size: "60px",
            }),
          ),
        ),
      ),
    );
  }

  const audioOpts = devices?.audio?.length
    ? [
        { value: "", label: "(auto-detect)" },
        ...devices.audio.map((d) => ({ value: d.name, label: d.name })),
      ]
    : [{ value: "", label: "(auto-detect)" }];

  const micCalBtn = el("button", {
    class: "secondary",
    type: "button",
    text: "Calibrate",
    onclick: () => runMicCalibration(cam.name),
  });
  const micCalRow = el("div", { class: "field" });
  micCalRow.appendChild(el("label", { text: "Calibrate" }));
  micCalRow.appendChild(micCalBtn);
  const micCalResult = el("div", {
    id: `mic-cal-${idx}`,
    class: "mic-cal-result",
  });

  body.appendChild(
    subsection(
      "Microphone",
      grid(
        field("Enabled", chkInput(cam.mic?.enabled ?? false, p("mic.enabled"))),
        field(
          "Device",
          selectInput(
            cam.mic?.device_name ?? "",
            p("mic.device_name"),
            audioOpts,
          ),
        ),
      ),
      grid(
        field(
          "Gain",
          numInput(cam.mic?.gain ?? 1, p("mic.gain"), {
            min: 0,
            max: 20,
            step: 0.1,
            size: "60px",
          }),
        ),
        field(
          "DC block",
          chkInput(cam.mic?.dc_block ?? true, p("mic.dc_block")),
        ),
        field(
          "Noise gate",
          numInput(cam.mic?.noise_gate ?? 0, p("mic.noise_gate"), {
            min: 0,
            size: "80px",
          }),
        ),
      ),
      advancedMode
        ? grid(
            field(
              "Sample rate",
              numInput(cam.mic?.sample_rate ?? 48000, p("mic.sample_rate"), {
                min: 8000,
                size: "80px",
              }),
            ),
            field(
              "Channels",
              numInput(cam.mic?.channels ?? 1, p("mic.channels"), {
                min: 1,
                max: 8,
                size: "60px",
              }),
            ),
            field(
              "Backend",
              selectInput(cam.mic?.backend ?? "wasapi", p("mic.backend"), [
                { value: "wasapi", label: "WASAPI" },
                { value: "auto", label: "Auto" },
              ]),
            ),
          )
        : null,
      micCalRow,
      micCalResult,
    ),
  );

  const zoneCount = (cam.motion?.zones || []).length;
  const zoneRow = el("div", { class: "field" });
  const zoneBtn = el("button", {
    class: "secondary",
    type: "button",
    text: zoneCount ? `Edit zones (${zoneCount})` : "Edit zones",
    onclick: () => openZoneEditor(idx),
  });
  zoneRow.appendChild(el("label", { text: "Zones" }));
  zoneRow.appendChild(zoneBtn);

  body.appendChild(
    subsection(
      "Motion",
      grid(
        field(
          "Enabled",
          chkInput(cam.motion?.enabled ?? true, p("motion.enabled")),
        ),
        field(
          "Detection",
          selectInput(cam.detection ?? "rawvideo", p("detection"), [
            { value: "rawvideo", label: "Raw (fast)" },
            { value: "jpeg", label: "JPEG (MJPEG)" },
          ]),
        ),
      ),
      grid(
        field(
          "Width",
          numInput(cam.motion?.width ?? 320, p("motion.width"), {
            min: 32,
            max: 1920,
            size: "60px",
          }),
        ),
        field(
          "Height",
          numInput(cam.motion?.height ?? 180, p("motion.height"), {
            min: 32,
            max: 1080,
            size: "60px",
          }),
        ),
        field(
          "FPS",
          numInput(cam.motion?.fps ?? 10, p("motion.fps"), {
            min: 1,
            max: 30,
            size: "60px",
          }),
        ),
      ),
      advancedMode
        ? grid(
            field(
              "Threshold",
              numInput(cam.motion?.threshold ?? 25, p("motion.threshold"), {
                min: 1,
                max: 100,
                size: "60px",
              }),
            ),
            field(
              "Min pixels",
              numInput(cam.motion?.min_pixels ?? 250, p("motion.min_pixels"), {
                min: 1,
                max: 100000,
                size: "80px",
              }),
            ),
            field(
              "Bg alpha",
              numInput(cam.motion?.bg_alpha ?? 0.05, p("motion.bg_alpha"), {
                min: 0,
                max: 1,
                step: 0.01,
                size: "60px",
              }),
            ),
            field(
              "Post-roll (s)",
              numInput(
                cam.motion?.post_roll_sec ?? 3,
                p("motion.post_roll_sec"),
                { min: 0, step: 0.5, size: "60px" },
              ),
            ),
            field(
              "Event cap (s)",
              numInput(
                cam.motion?.event_cap_sec ?? 900,
                p("motion.event_cap_sec"),
                { min: 10, size: "80px" },
              ),
            ),
            field(
              "Restart backoff",
              numInput(
                cam.motion?.restart_backoff_sec ?? 2,
                p("motion.restart_backoff_sec"),
                { min: 0, step: 0.5, size: "60px" },
              ),
            ),
          )
        : null,
      zoneRow,
    ),
  );

  if (advancedMode) {
    body.appendChild(
      subsection(
        "Sound",
        grid(
          field(
            "Enabled",
            chkInput(cam.sound?.enabled ?? true, p("sound.enabled")),
          ),
          field(
            "Abs floor",
            numInput(cam.sound?.abs_floor ?? 250, p("sound.abs_floor"), {
              min: 0,
              size: "80px",
            }),
          ),
          field(
            "Ratio",
            numInput(cam.sound?.ratio ?? 3, p("sound.ratio"), {
              min: 0,
              step: 0.1,
              size: "60px",
            }),
          ),
        ),
        grid(
          field(
            "Tau",
            numInput(cam.sound?.tau ?? 0.02, p("sound.tau"), {
              min: 0,
              step: 0.001,
              size: "60px",
            }),
          ),
          field(
            "Hold blocks",
            numInput(cam.sound?.hold_blocks ?? 4, p("sound.hold_blocks"), {
              min: 0,
              size: "60px",
            }),
          ),
          field(
            "Drop blocks",
            numInput(cam.sound?.drop_blocks ?? 30, p("sound.drop_blocks"), {
              min: 0,
              size: "60px",
            }),
          ),
        ),
      ),
    );
    body.appendChild(
      subsection(
        "Image",
        grid(
          field(
            "Brightness",
            numInput(cam.image?.brightness ?? 0, p("image.brightness"), {
              min: -1,
              max: 1,
              step: 0.1,
              size: "60px",
            }),
          ),
          field(
            "Contrast",
            numInput(cam.image?.contrast ?? 0, p("image.contrast"), {
              min: -1,
              max: 1,
              step: 0.1,
              size: "60px",
            }),
          ),
          field(
            "Saturation",
            numInput(cam.image?.saturation ?? 0, p("image.saturation"), {
              min: -1,
              max: 1,
              step: 0.1,
              size: "60px",
            }),
          ),
          field(
            "Hue",
            numInput(cam.image?.hue ?? 0, p("image.hue"), {
              min: -1,
              max: 1,
              step: 0.1,
              size: "60px",
            }),
          ),
          field(
            "Gamma",
            numInput(cam.image?.gamma ?? 0, p("image.gamma"), {
              min: -1,
              max: 1,
              step: 0.1,
              size: "60px",
            }),
          ),
        ),
      ),
    );
  }

  body.appendChild(
    subsection(
      "Live Stream",
      grid(
        field(
          "Live codec",
          selectInput(
            cam.live?.video?.codec ?? "h264_nvenc",
            p("live.video.codec"),
            (() => {
              const lo = [{ value: "copy", label: "Copy (passthrough)" }];
              if (devices?.encoders) {
                for (const e of devices.encoders)
                  lo.push({ value: e.name, label: `${e.name} — ${e.info}` });
              } else {
                lo.push(
                  { value: "h264_nvenc", label: "H.264 NVENC" },
                  { value: "libx264", label: "H.264 (CPU)" },
                );
              }
              return lo;
            })(),
          ),
        ),
        field(
          "Live audio",
          selectInput(cam.live?.audio?.codec ?? "aac", p("live.audio.codec"), [
            { value: "aac", label: "AAC" },
            { value: "copy", label: "Copy" },
          ]),
        ),
        field("HLS", chkInput(cam.live?.hls ?? false, p("live.hls"))),
      ),
    ),
  );

  camSec.appendChild(body);
  return camSec;
}

// ---------------- add camera modal (presets) ----------------

const PRESETS = {
  "USB 720p": {
    source: "dshow",
    width: 1280,
    height: 720,
    fps: 10,
    mic: { enabled: true, gain: 2, dc_block: true },
    motion: {
      enabled: true,
      width: 320,
      height: 180,
      fps: 10,
      threshold: 25,
      min_pixels: 250,
      bg_alpha: 0.05,
      post_roll_sec: 3,
      event_cap_sec: 900,
      restart_backoff_sec: 2,
    },
    sound: {
      enabled: true,
      abs_floor: 250,
      ratio: 3,
      tau: 0.02,
      hold_blocks: 4,
      drop_blocks: 30,
    },
    record: {
      enabled: true,
      video: {
        codec: "h264_nvenc",
        preset: "p2",
        rc: "vbr",
        cq: 28,
        maxrate: "4M",
        g: 15,
      },
      audio: { codec: "aac", bitrate: "64k" },
    },
    priority: 5,
  },
  "USB 1080p": {
    source: "dshow",
    width: 1920,
    height: 1080,
    fps: 15,
    mic: { enabled: true, gain: 2, dc_block: true },
    motion: {
      enabled: true,
      width: 320,
      height: 180,
      fps: 10,
      threshold: 25,
      min_pixels: 250,
      bg_alpha: 0.05,
      post_roll_sec: 3,
      event_cap_sec: 900,
      restart_backoff_sec: 2,
    },
    sound: {
      enabled: true,
      abs_floor: 250,
      ratio: 3,
      tau: 0.02,
      hold_blocks: 4,
      drop_blocks: 30,
    },
    record: {
      enabled: true,
      video: {
        codec: "h264_nvenc",
        preset: "p2",
        rc: "vbr",
        cq: 28,
        maxrate: "4M",
        g: 15,
      },
      audio: { codec: "aac", bitrate: "64k" },
    },
    priority: 5,
  },
  RTSP: {
    source: "rtsp",
    width: 1920,
    height: 1080,
    fps: 15,
    mic: { enabled: false },
    motion: {
      enabled: true,
      width: 320,
      height: 180,
      fps: 10,
      threshold: 25,
      min_pixels: 250,
      bg_alpha: 0.05,
      post_roll_sec: 3,
      event_cap_sec: 900,
      restart_backoff_sec: 2,
    },
    sound: { enabled: false },
    record: {
      enabled: true,
      video: {
        codec: "copy",
        preset: "p2",
        rc: "vbr",
        cq: 28,
        maxrate: "4M",
        g: 15,
      },
      audio: { codec: "copy", bitrate: "64k" },
    },
    priority: 5,
  },
  "RTSP + mic": {
    source: "rtsp",
    width: 1920,
    height: 1080,
    fps: 15,
    mic: { enabled: true, gain: 2, dc_block: true },
    motion: {
      enabled: true,
      width: 320,
      height: 180,
      fps: 10,
      threshold: 25,
      min_pixels: 250,
      bg_alpha: 0.05,
      post_roll_sec: 3,
      event_cap_sec: 900,
      restart_backoff_sec: 2,
    },
    sound: {
      enabled: true,
      abs_floor: 250,
      ratio: 3,
      tau: 0.02,
      hold_blocks: 4,
      drop_blocks: 30,
    },
    record: {
      enabled: true,
      video: {
        codec: "copy",
        preset: "p2",
        rc: "vbr",
        cq: 28,
        maxrate: "4M",
        g: 15,
      },
      audio: { codec: "aac", bitrate: "64k" },
    },
    priority: 5,
  },
  Blank: {
    source: "dshow",
    width: 1920,
    height: 1080,
    fps: 15,
    mic: { enabled: false },
    motion: { enabled: false },
    sound: { enabled: false },
    record: { enabled: false },
    priority: 5,
  },
};

function showAddCameraModal() {
  const overlay = el("div", { class: "modal-overlay" });
  const modal = el("div", { class: "modal" });
  modal.appendChild(el("h2", { text: "Add camera" }));
  modal.appendChild(
    el("p", { class: "modal-desc", text: "Choose a preset or start blank:" }),
  );

  const grid = el("div", { class: "preset-grid" });
  for (const [name, preset] of Object.entries(PRESETS)) {
    const card = el("button", {
      class: "preset-card",
      text: name,
      onclick: () => {
        addCameraWithPreset(name, preset);
        overlay.remove();
      },
    });
    grid.appendChild(card);
  }
  modal.appendChild(grid);
  modal.appendChild(
    el("button", {
      class: "secondary",
      text: "Cancel",
      style: "margin-top:12px",
      onclick: () => overlay.remove(),
    }),
  );
  overlay.appendChild(modal);
  overlay.addEventListener("click", (e) => {
    if (e.target === overlay) overlay.remove();
  });
  document.body.appendChild(overlay);
}

function addCameraWithPreset(_name, preset) {
  currentConfig.cameras = currentConfig.cameras || [];
  const idx = currentConfig.cameras.length;
  const camName = `cam${idx + 1}`;
  const cam = {
    name: camName,
    device_name: "",
    rtsp_url: "",
    ...JSON.parse(JSON.stringify(preset)),
  };
  cam.record.prefix = camName;
  currentConfig.cameras.push(cam);
  renderConfigForm(currentConfig);
}

// ---------------- collect form values ----------------

function collectForm() {
  const cfg = JSON.parse(JSON.stringify(currentConfig));
  const inputs = document.querySelectorAll("#cfg-form [data-name]");
  for (const inp of inputs) {
    const name = inp.dataset.name;
    if (name.endsWith(".__preset")) continue;
    const parts = name.split(".");
    let obj = cfg;
    for (let i = 0; i < parts.length - 1; i++) {
      if (obj == null) break;
      const k = parts[i];
      const idx = /^\d+$/.test(k) ? parseInt(k, 10) : null;
      if (idx !== null) obj = obj[idx];
      else obj = obj[k];
    }
    if (obj == null) continue;
    const key = parts[parts.length - 1];
    if (inp.type === "checkbox") obj[key] = inp.checked;
    else if (inp.type === "number") {
      const v = parseFloat(inp.value);
      if (!Number.isNaN(v)) obj[key] = v;
    } else obj[key] = inp.value;
  }
  return cfg;
}

// ---------------- unsaved tracking ----------------

function getValByPath(obj, path) {
  const parts = path.split(".");
  let cur = obj;
  for (const p of parts) {
    if (cur == null) return undefined;
    const idx = /^\d+$/.test(p) ? parseInt(p, 10) : null;
    cur = idx !== null ? cur[idx] : cur[p];
  }
  return cur;
}

function showDiffDialog(changes) {
  const fmtVal = (v) => {
    if (v === null || v === undefined) return "(empty)";
    if (typeof v === "object") {
      const s = JSON.stringify(v);
      return s.length > 80 ? `${s.slice(0, 77)}...` : s;
    }
    return String(v);
  };
  return new Promise((resolve) => {
    const overlay = el("div", { class: "modal-overlay" });
    const modal = el("div", { class: "modal diff-modal" });
    modal.appendChild(el("h2", { text: "Confirm changes" }));
    modal.appendChild(
      el("p", {
        class: "modal-desc",
        text: `${changes.length} setting${changes.length === 1 ? "" : "s"} will be changed:`,
      }),
    );

    const list = el("div", { class: "diff-list" });
    for (const c of changes) {
      const row = el("div", { class: "diff-item" });
      row.appendChild(el("span", { class: "diff-path", text: c.path }));
      const vals = el("span", { class: "diff-vals" });
      vals.appendChild(el("span", { class: "diff-old", text: fmtVal(c.old) }));
      vals.appendChild(el("span", { text: " \u2192 " }));
      vals.appendChild(el("span", { class: "diff-new", text: fmtVal(c.new) }));
      row.appendChild(vals);
      list.appendChild(row);
    }
    modal.appendChild(list);

    const btnRow = el("div", { class: "row", style: "margin-top:12px" });
    btnRow.appendChild(
      el("button", {
        text: "Save",
        onclick: () => {
          overlay.remove();
          resolve(true);
        },
      }),
    );
    btnRow.appendChild(
      el("button", {
        class: "secondary",
        text: "Cancel",
        onclick: () => {
          overlay.remove();
          resolve(false);
        },
      }),
    );
    modal.appendChild(btnRow);
    overlay.appendChild(modal);
    overlay.addEventListener("click", (e) => {
      if (e.target === overlay) {
        overlay.remove();
        resolve(false);
      }
    });
    document.body.appendChild(overlay);
  });
}

function updateFieldIndicators() {
  if (!savedSnapshot) return;
  const inputs = document.querySelectorAll("#cfg-form [data-name]");
  for (const inp of inputs) {
    if (inp.dataset.name.endsWith(".__preset")) continue;
    const field = inp.closest(".field");
    if (!field) continue;
    const saved = getValByPath(savedSnapshot, inp.dataset.name);
    let cur;
    if (inp.type === "checkbox") cur = inp.checked;
    else if (inp.type === "number") {
      cur = parseFloat(inp.value);
      if (Number.isNaN(cur)) cur = inp.value;
    } else cur = inp.value;
    const changed = JSON.stringify(saved) !== JSON.stringify(cur);
    field.classList.toggle("changed", changed);
  }
}

function hasFieldChanged(inp) {
  if (inp.dataset.name.endsWith(".__preset")) return false;
  const saved = getValByPath(savedSnapshot, inp.dataset.name);
  if (saved === undefined) return false;
  let cur;
  if (inp.type === "checkbox") cur = inp.checked;
  else if (inp.type === "number") {
    cur = parseFloat(inp.value);
    if (Number.isNaN(cur)) cur = inp.value;
  } else cur = inp.value;
  return JSON.stringify(saved) !== JSON.stringify(cur);
}

function updateUnsavedDot() {
  const dot = $("#unsaved-dot");
  if (!dot || !savedSnapshot) return;
  const inputs = document.querySelectorAll("#cfg-form [data-name]");
  let changed = false;
  for (const inp of inputs) {
    if (hasFieldChanged(inp)) {
      changed = true;
      break;
    }
  }
  dot.hidden = !changed;
  updateFieldIndicators();
}

// Listen for changes on form inputs (not keystrokes — #4f)
document.addEventListener(
  "change",
  (e) => {
    if (e.target.closest("#cfg-form")) updateUnsavedDot();
    // When device dropdown changes, fetch new formats and update resolution select
    if (e.target.dataset?.name?.endsWith(".device_name") && e.target.value) {
      const nameParts = e.target.dataset.name.split(".");
      const idx = parseInt(nameParts[1], 10);
      if (!Number.isNaN(idx)) {
        fetchDeviceFormats(e.target.value).then((formats) => {
          if (!formats || formats.length === 0) return;
          const cam = currentConfig?.cameras?.[idx];
          if (!cam) return;
          const form = $("#cfg-form");
          const wName = `cameras.${idx}.width`;
          const hName = `cameras.${idx}.height`;
          const oldSelect = form?.querySelector(
            `[data-name="${wName}.__preset"]`,
          );
          if (!oldSelect) return;
          const newSelect = resolutionSelect(
            cam.width ?? 1920,
            cam.height ?? 1080,
            wName,
            hName,
            formats,
          );
          oldSelect.replaceWith(newSelect);
        });
      }
    }
  },
  true,
);

// ---------------- status ----------------

let _lastStatusJSON = "";
let _serverStartWall = 0; // Date.now() when server reported uptime
let _uptimeInterval = null;

function tickUptime() {
  const td = $("#uptime-val");
  if (!td || !_serverStartWall) return;
  td.textContent = fmtDuration((Date.now() - _serverStartWall) * 1e6);
}

async function renderStatus() {
  const box = $("#tab-status");
  if (!_lastStatusJSON)
    box.replaceChildren(el("div", { class: "card", text: "Loading\u2026" }));
  let s;
  try {
    s = await api("/api/status");
  } catch (e) {
    if (!_lastStatusJSON) {
      box.replaceChildren(el("div", { class: "card err", text: e.message }));
    }
    return;
  }

  // Compute server start wall time for local uptime ticking.
  _serverStartWall = Date.now() - Number(s.uptime_ns || 0) / 1e6;
  if (!_uptimeInterval) {
    _uptimeInterval = setInterval(tickUptime, 1000);
  }

  // Skip rebuild if only uptime changed — compare everything else.
  const snapshot = { ...s, uptime_ns: 0 };
  const json = JSON.stringify(snapshot);
  if (json === _lastStatusJSON) return;
  _lastStatusJSON = json;

  $("#live-link").href = `http://${location.hostname}:${s.live_port}`;
  box.replaceChildren();
  const g = s.go2rtc;

  const camRows = (s.cameras || []).map((c) =>
    el(
      "tr",
      el("td", { text: c.name }),
      el("td", {
        class: c.active ? "up" : "down",
        text: c.active ? "running" : "stopped",
      }),
      el("td", { text: c.mic ? "on" : "off" }),
      el("td", { text: c.mode }),
    ),
  );

  const sys = s.system || {};

  box.append(
    el(
      "div",
      { class: "card" },
      el("h2", { text: "Service" }),
      el(
        "table",
        {},
        el(
          "tr",
          {},
          el("td", { text: "Version" }),
          el("td", { text: s.version }),
        ),
        el(
          "tr",
          {},
          el("td", { text: "Uptime" }),
          el("td", { id: "uptime-val", text: fmtDuration(s.uptime_ns) }),
        ),
        el(
          "tr",
          {},
          el("td", { text: "Config" }),
          el("td", { class: "mono", text: s.config_path }),
        ),
        el(
          "tr",
          {},
          el("td", { text: "Record mode" }),
          el("td", { text: s.mode }),
        ),
        el(
          "tr",
          {},
          el("td", { text: "go2rtc" }),
          el(
            "td",
            {},
            el("span", {
              class: g.running ? "up" : "down",
              text: g.running ? "running" : "stopped",
            }),
            g.running ? ` (pid ${g.pid})` : "",
          ),
        ),
      ),
      adminControls(),
    ),
    el(
      "div",
      { class: "card" },
      el("h2", { text: "Cameras" }),
      el(
        "table",
        {},
        el(
          "tr",
          {},
          el("th", { text: "Name" }),
          el("th", { text: "Runner" }),
          el("th", { text: "Mic" }),
          el("th", { text: "Mode" }),
        ),
        ...camRows,
      ),
    ),
    renderSystemInfo(sys),
  );
}

function fmtDuration(ns) {
  if (!ns) return "0s";
  const s = Math.floor(ns / 1e9);
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
}

function renderSystemInfo(sys) {
  if (!sys || (!sys.memory && !sys.disk)) return null;
  const rows = [];
  if (sys.memory) {
    const m = sys.memory;
    rows.push(
      el(
        "tr",
        {},
        el("td", { text: "Memory" }),
        el("td", {
          html: `<span class="${m.used_pct > 85 ? "down" : ""}">${m.used_pct?.toFixed(0) ?? "?"}%</span> used / ${fmtSize(m.total ?? 0)}`,
        }),
      ),
    );
  }
  if (sys.disk) {
    const d = sys.disk;
    rows.push(
      el(
        "tr",
        {},
        el("td", { text: "Disk" }),
        el("td", {
          html: `<span class="${d.used_pct > 90 ? "down" : ""}">${d.used_pct?.toFixed(0) ?? "?"}%</span> used / ${fmtSize(d.total ?? 0)}`,
        }),
      ),
    );
  }
  if (!rows.length) return null;
  return el(
    "div",
    { class: "card" },
    el("h2", { text: "System" }),
    el("table", {}, ...rows),
  );
}

// ---------------- admin ----------------

function adminControls() {
  return el(
    "div",
    { class: "row" },
    el("button", {
      class: "danger",
      text: "Restart service",
      onclick: (e) => withButtonLoading(e.target, restartService)(),
    }),
    el("button", {
      class: "secondary",
      text: "Run retention",
      onclick: (e) => withButtonLoading(e.target, runRetention)(),
    }),
    el("button", {
      class: "secondary",
      text: "Stop all",
      onclick: (e) => withButtonLoading(e.target, stopAll)(),
    }),
    el("button", {
      class: "secondary",
      text: "Start all",
      onclick: (e) => withButtonLoading(e.target, startAll)(),
    }),
  );
}

async function restartService() {
  if (
    !confirm("Restart the service now? The panel will be briefly unavailable.")
  )
    return;
  try {
    await api("/api/restart", { method: "POST" });
    toast("Service restarting\u2026", "success");
  } catch (e) {
    toast(e.message, "error");
  }
}

async function runRetention() {
  if (!confirm("Run retention now? This may delete recordings.")) return;
  try {
    await api("/api/retention/run", { method: "POST" });
    toast("Retention completed", "success");
  } catch (e) {
    toast(e.message, "error");
  }
}

async function stopAll() {
  if (!confirm("Stop all cameras?")) return;
  try {
    await api("/api/stop-all", { method: "POST" });
    toast("All cameras stopped", "success");
    setTimeout(refresh, 1000);
  } catch (e) {
    toast(e.message, "error");
  }
}

async function startAll() {
  try {
    await api("/api/start-all", { method: "POST" });
    toast("All cameras started", "success");
    setTimeout(refresh, 1000);
  } catch (e) {
    toast(e.message, "error");
  }
}

// ---------------- logs ----------------

let _lastLogName = "";
let _lastLogText = "";
async function renderLogs() {
  const name = $("#log-name").value;
  const msg = $("#log-msg");
  const pre = $("#log-view");
  if (!_lastLogText || name !== _lastLogName) {
    pre.textContent = "Loading\u2026";
  }
  try {
    const res = await fetch(
      `/api/logs?name=${encodeURIComponent(name)}&lines=1000`,
    );
    if (!res.ok) {
      let data = await res.text();
      try {
        data = JSON.parse(data).error || data;
      } catch (_) {}
      throw new Error(data);
    }
    const text = await res.text();
    if (name !== _lastLogName || text !== _lastLogText) {
      _lastLogName = name;
      _lastLogText = text;
      pre.textContent = text;
      pre.scrollTop = pre.scrollHeight;
    }
    msg.textContent = "";
    msg.className = "";
  } catch (e) {
    msg.textContent = e.message;
    msg.className = "err";
  }
}
$("#log-refresh").addEventListener("click", renderLogs);
$("#log-name").addEventListener("change", renderLogs);

// ---------------- recordings ----------------

const REC_PAGE = 100;
let recordingList = [];
let recordingTotal = 0;

function recordingSrc(rec) {
  return (
    "/api/recordings/" +
    encodeURIComponent(rec.camera) +
    "/" +
    encodeURIComponent(rec.name)
  );
}

function highlightRecRow(idx) {
  document.querySelectorAll("#rec-list tr.rec-row").forEach((r) => {
    r.classList.remove("playing");
  });
  const row = document.querySelector(`#rec-list tr.rec-row[data-idx="${idx}"]`);
  if (row) row.classList.add("playing");
}

function renderRecTable(box) {
  box.replaceChildren();
  if (!recordingList.length) {
    box.append(el("div", { class: "card", text: "No recordings yet." }));
    return;
  }
  const byCam = new Map();
  for (let i = 0; i < recordingList.length; i++) {
    const rec = recordingList[i];
    if (!byCam.has(rec.camera)) byCam.set(rec.camera, []);
    byCam.get(rec.camera).push({ rec, idx: i });
  }
  for (const [cam, recs] of byCam) {
    box.append(
      el(
        "div",
        { class: "card" },
        el("h2", { text: `${cam} (${recs.length})` }),
        el(
          "table",
          {},
          el(
            "tr",
            {},
            el("th", { text: "File" }),
            el("th", { text: "Recorded" }),
            el("th", { text: "Size" }),
          ),
          ...recs.map(({ rec, idx }) =>
            el(
              "tr",
              { class: "rec-row", "data-idx": String(idx) },
              el(
                "td",
                {},
                el("a", {
                  class: "play",
                  text: rec.name,
                  onclick: () => play(idx),
                }),
              ),
              el("td", { text: fmtTime(rec.mod_time) }),
              el("td", { text: fmtSize(rec.size) }),
            ),
          ),
        ),
      ),
    );
  }
  if (recordingTotal > recordingList.length) {
    const remaining = recordingTotal - recordingList.length;
    box.append(
      el(
        "div",
        { class: "card", style: "text-align:center;padding:8px" },
        el("button", {
          class: "secondary",
          text: `Load more (${remaining} remaining)`,
          onclick: () => loadMoreRecs(box),
        }),
      ),
    );
  }
}

async function loadMoreRecs(box) {
  try {
    const res = await api(
      `/api/recordings?offset=${recordingList.length}&limit=${REC_PAGE}`,
    );
    recordingList = recordingList.concat(res.items);
    recordingTotal = res.total;
    renderRecTable(box);
  } catch (e) {
    toast(`Failed to load recordings: ${e.message}`, "error");
  }
}

let _lastRecTotal = -1;
let _lastRecModTime = "";
async function renderRecordings() {
  const box = $("#rec-list");
  if (_lastRecTotal < 0)
    box.replaceChildren(el("div", { class: "card", text: "Loading\u2026" }));
  const res = await api(`/api/recordings?limit=${REC_PAGE}`);
  const items = res.items || [];
  const total = res.total || 0;
  const lastMod = items.length ? items[0].mod_time : "";
  if (total === _lastRecTotal && lastMod === _lastRecModTime) return;
  _lastRecTotal = total;
  _lastRecModTime = lastMod;

  recordingList = items;
  recordingTotal = total;
  renderRecTable(box);
}

function play(idx) {
  const rec = recordingList[idx];
  if (!rec) return;
  const overlay = el("div", { class: "modal-overlay" });
  const modal = el("div", { class: "modal video-modal" });
  const title = el("h2", { text: rec.name });
  modal.appendChild(title);

  const player = document.createElement("video");
  player.controls = true;
  player.autoplay = true;
  player.volume = 0.5;
  player.muted = true;
  player.style.width = "100%";
  player.style.borderRadius = "8px";
  player.src = recordingSrc(rec);

  modal.appendChild(player);

  const desc = el("p", {
    class: "modal-desc",
    text: `${rec.camera} \u2014 ${fmtTime(rec.mod_time)} \u2014 ${fmtSize(rec.size)}`,
  });
  modal.appendChild(desc);

  const btnRow = el("div", { class: "row", style: "margin-top:12px;gap:8px" });
  const prevBtn = el("button", {
    class: "secondary",
    text: "\u2190 Prev",
    onclick: () => nav(-1),
  });
  const nextBtn = el("button", {
    class: "secondary",
    text: "Next \u2192",
    onclick: () => nav(1),
  });
  const muteBtn = el("button", {
    class: "secondary",
    text: "Unmute",
    onclick: () => {
      player.muted = !player.muted;
      muteBtn.textContent = player.muted ? "Unmute" : "Mute";
    },
  });
  prevBtn.disabled = idx === 0;
  nextBtn.disabled = idx === recordingList.length - 1;
  btnRow.appendChild(prevBtn);
  btnRow.appendChild(nextBtn);
  btnRow.appendChild(muteBtn);
  btnRow.appendChild(el("span", { style: "flex:1" }));
  btnRow.appendChild(
    el("button", { class: "secondary", text: "Close", onclick: close }),
  );
  modal.appendChild(btnRow);
  overlay.appendChild(modal);
  overlay.addEventListener("click", (e) => {
    if (e.target === overlay) close();
  });
  document.body.appendChild(overlay);

  // Keyboard shortcuts — document capture fires before anything else.
  function onKey(e) {
    if (e.target.tagName === "INPUT" || e.target.tagName === "TEXTAREA") return;
    if (e.key === "ArrowLeft") {
      e.preventDefault();
      e.stopPropagation();
      nav(-1);
    } else if (e.key === "ArrowRight") {
      e.preventDefault();
      e.stopPropagation();
      nav(1);
    } else if (e.key === " ") {
      e.preventDefault();
      e.stopPropagation();
      player.paused ? player.play() : player.pause();
    } else if (e.key === "m" || e.key === "M") {
      player.muted = !player.muted;
      muteBtn.textContent = player.muted ? "Unmute" : "Mute";
    } else if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      close();
    }
  }
  document.addEventListener("keydown", onKey, true);

  highlightRecRow(idx);
  player.play().catch(() => {});

  function nav(delta) {
    const next = idx + delta;
    if (next < 0 || next >= recordingList.length) return;
    player.pause();
    idx = next;
    const r = recordingList[idx];
    title.textContent = r.name;
    player.src = recordingSrc(r);
    desc.textContent = `${r.camera} \u2014 ${fmtTime(r.mod_time)} \u2014 ${fmtSize(r.size)}`;
    prevBtn.disabled = idx === 0;
    nextBtn.disabled = idx === recordingList.length - 1;
    highlightRecRow(idx);
    player.play().catch(() => {});
  }

  function close() {
    player.pause();
    document.removeEventListener("keydown", onKey, true);
    overlay.remove();
  }
}

// ---------------- config ----------------

async function loadConfig() {
  const cfg = await api("/api/config");
  currentConfig = cfg;
  savedSnapshot = JSON.parse(JSON.stringify(cfg));
  devices = await getDevices();
  renderConfigForm(cfg);
  setViewMode("form");
}

function setViewMode(mode) {
  const formWrap = $("#cfg-form");
  const textWrap = $("#cfg-text-wrap");
  const btnForm = $("#cfg-view-form");
  const btnRaw = $("#cfg-view-raw");
  if (mode === "form") {
    formWrap.hidden = false;
    textWrap.hidden = true;
    btnForm.classList.add("active");
    btnRaw.classList.remove("active");
  } else {
    formWrap.hidden = true;
    textWrap.hidden = false;
    btnForm.classList.remove("active");
    btnRaw.classList.add("active");
    if (currentConfig) {
      $("#cfg-text").value = JSON.stringify(currentConfig, null, 2);
    }
  }
}

function toggleAdvanced() {
  advancedMode = !advancedMode;
  const btn = $("#cfg-adv-toggle");
  btn.textContent = advancedMode ? "Simple" : "Advanced";
  btn.classList.toggle("active", advancedMode);
  if (currentConfig) renderConfigForm(currentConfig);
}

$("#cfg-view-form").addEventListener("click", () => setViewMode("form"));
$("#cfg-view-raw").addEventListener("click", () => setViewMode("raw"));
$("#cfg-adv-toggle").addEventListener("click", toggleAdvanced);

// ---------------- validation ----------------

function validateConfig(cfg) {
  const warnings = [];
  const errors = [];

  if (!cfg.cameras || cfg.cameras.length === 0) {
    warnings.push("No cameras configured.");
  }

  const usedPorts = new Set();
  for (const [_k, v] of Object.entries(cfg.go2rtc || {})) {
    if (typeof v === "number" && v > 0) usedPorts.add(v);
  }
  if (typeof cfg.web?.port === "number") usedPorts.add(cfg.web.port);

  for (let i = 0; i < (cfg.cameras || []).length; i++) {
    const cam = cfg.cameras[i];
    const tag = `Camera ${i + 1} (${cam.name || "unnamed"})`;

    if (!cam.name?.trim()) errors.push(`${tag}: name is empty.`);
    if (!cam.source) errors.push(`${tag}: source is required.`);
    if (cam.source === "rtsp" && !cam.rtsp_url)
      warnings.push(`${tag}: RTSP source has no URL.`);

    if (cam.source === "dshow" && !cam.device_name) {
      warnings.push(`${tag}: no device selected (will auto-detect).`);
    }

    if (cam.record?.enabled && !cam.record.out_dir && !cfg.paths?.record_dir) {
      warnings.push(
        `${tag}: recording enabled but no output dir set (will use ".").`,
      );
    }

    if (cam.mic?.enabled && !cam.mic?.device_name) {
      warnings.push(`${tag}: mic enabled but no device (will auto-detect).`);
    }

    if (cam.mic?.enabled) {
      if (cam.mic.gain > 20)
        warnings.push(
          `${tag}: mic gain ${cam.mic.gain} is very high, will clip. Max 20.`,
        );
      if (cam.mic.gain < 0) errors.push(`${tag}: mic gain must be >= 0.`);
      if (
        cam.mic.sample_rate &&
        ![8000, 16000, 22050, 44100, 48000].includes(cam.mic.sample_rate)
      ) {
        warnings.push(`${tag}: unusual sample rate ${cam.mic.sample_rate} Hz.`);
      }
    }

    for (const p of [
      cam.mic?.live_port,
      cam.mic?.rec_port,
      cam.mic?.ctl_port,
    ]) {
      if (typeof p === "number" && p > 0) {
        if (p < 1024)
          warnings.push(
            `${tag}: port ${p} is privileged (< 1024), may fail without admin.`,
          );
        if (usedPorts.has(p))
          errors.push(`${tag}: port ${p} conflicts with another service.`);
        usedPorts.add(p);
      }
    }

    if (cam.priority !== undefined) {
      if (cam.priority < 0) errors.push(`${tag}: priority must be >= 0.`);
      if (cam.priority > 10)
        warnings.push(
          `${tag}: priority ${cam.priority} is very high (10 = critical).`,
        );
    }

    if (cam.record?.enabled && cam.record.segment_sec !== undefined) {
      if (cam.record.segment_sec < 1)
        warnings.push(
          `${tag}: segment_sec < 1s will create very many small files.`,
        );
      if (cam.record.segment_sec > 7200)
        warnings.push(`${tag}: segment_sec > 2h makes seeking slow.`);
    }

    if (cam.retain_hours !== undefined) {
      if (cam.retain_hours < 1)
        warnings.push(
          `${tag}: retain_hours < 1 will delete recordings very quickly.`,
        );
      if (cam.retain_hours > 720)
        warnings.push(
          `${tag}: retain_hours > 30 days, disk usage may grow large.`,
        );
    }

    const motion = cam.motion || {};
    if (motion.threshold !== undefined) {
      if (motion.threshold < 1)
        errors.push(`${tag}: motion threshold must be >= 1.`);
      if (motion.threshold > 100)
        warnings.push(`${tag}: motion threshold > 100 will rarely trigger.`);
    }
    if (motion.min_pixels !== undefined && motion.min_pixels < 0) {
      errors.push(`${tag}: motion min_pixels must be >= 0.`);
    }
    if (motion.bg_alpha !== undefined) {
      if (motion.bg_alpha <= 0 || motion.bg_alpha > 1) {
        errors.push(
          `${tag}: motion bg_alpha must be between 0 (exclusive) and 1.`,
        );
      }
      if (motion.bg_alpha > 0.2)
        warnings.push(
          `${tag}: motion bg_alpha > 0.2 adapts too fast, may miss slow changes.`,
        );
    }
    if (motion.width !== undefined && motion.height !== undefined) {
      if (motion.width > 1920 || motion.height > 1080) {
        warnings.push(
          `${tag}: detection resolution > 1080p wastes CPU. 320x180 is enough for motion.`,
        );
      }
    }
    if (motion.fps !== undefined) {
      if (motion.fps > 30) errors.push(`${tag}: detection FPS must be <= 30.`);
      if (motion.fps < 1) errors.push(`${tag}: detection FPS must be >= 1.`);
      if (motion.fps > 15)
        warnings.push(
          `${tag}: detection FPS > 15 wastes CPU. 5-10 is usually enough.`,
        );
    }
  }

  if (
    cfg.record?.mode === "none" &&
    cfg.cameras?.some((c) => c.record?.enabled)
  ) {
    warnings.push(
      "Global mode is 'none' but some cameras have recording enabled. Recording will be ignored.",
    );
  }

  if (cfg.web?.port !== undefined) {
    if (cfg.web.port < 1024)
      warnings.push("Web port < 1024 is privileged, may fail without admin.");
  }

  return { warnings, errors };
}

function showValidationDialog(warnings, errors) {
  return new Promise((resolve) => {
    const overlay = el("div", { class: "modal-overlay" });
    const modal = el("div", { class: "modal" });
    modal.appendChild(
      el("h2", { text: errors.length ? "Save blocked" : "Warnings" }),
    );

    const list = el("div", { class: "validation-list" });
    for (const e of errors)
      list.appendChild(el("div", { class: "validation-item err", text: e }));
    for (const w of warnings)
      list.appendChild(el("div", { class: "validation-item", text: w }));
    modal.appendChild(list);

    const btnRow = el("div", { class: "row", style: "margin-top:12px" });
    if (errors.length === 0) {
      btnRow.appendChild(
        el("button", {
          text: "Save anyway",
          onclick: () => {
            overlay.remove();
            resolve(true);
          },
        }),
      );
      btnRow.appendChild(
        el("button", {
          class: "secondary",
          text: "Cancel",
          onclick: () => {
            overlay.remove();
            resolve(false);
          },
        }),
      );
    } else {
      btnRow.appendChild(
        el("button", {
          class: "secondary",
          text: "Fix issues",
          onclick: () => {
            overlay.remove();
            resolve(false);
          },
        }),
      );
    }
    modal.appendChild(btnRow);
    overlay.appendChild(modal);
    overlay.addEventListener("click", (e) => {
      if (e.target === overlay) {
        overlay.remove();
        resolve(false);
      }
    });
    document.body.appendChild(overlay);
  });
}

$("#cfg-save").addEventListener("click", async () => {
  try {
    let cfg;
    const formWrap = $("#cfg-form");
    if (!formWrap.hidden) {
      cfg = collectForm();
    } else {
      const text = $("#cfg-text").value;
      try {
        cfg = JSON.parse(text);
      } catch (e) {
        throw new Error(`Invalid JSON: ${e.message}`);
      }
    }

    if (savedSnapshot) {
      const changes = [];
      const inputs = document.querySelectorAll("#cfg-form [data-name]");
      for (const inp of inputs) {
        if (!hasFieldChanged(inp)) continue;
        const saved = getValByPath(savedSnapshot, inp.dataset.name);
        let cur;
        if (inp.type === "checkbox") cur = inp.checked;
        else if (inp.type === "number") {
          cur = parseFloat(inp.value);
          if (Number.isNaN(cur)) cur = inp.value;
        } else cur = inp.value;
        changes.push({ path: inp.dataset.name, old: saved, new: cur });
      }
      if (changes.length > 0) {
        const ok = await showDiffDialog(changes);
        if (!ok) return;
      }
    }

    const { warnings, errors } = validateConfig(cfg);
    if (warnings.length > 0 || errors.length > 0) {
      const ok = await showValidationDialog(warnings, errors);
      if (!ok) return;
    }

    await api("/api/config", {
      method: "PUT",
      headers: { "Content-Type": "application/jsonc" },
      body: JSON.stringify(cfg, null, 2),
    });
    currentConfig = cfg;
    savedSnapshot = JSON.parse(JSON.stringify(cfg));
    updateUnsavedDot();
    toast("Config saved", "success");
  } catch (e) {
    toast(e.message, "error");
  }
});

$("#cfg-reload").addEventListener("click", loadConfig);

// ---------------- export / import ----------------

$("#cfg-export")?.addEventListener("click", () => {
  const cfg = collectForm();
  const blob = new Blob([JSON.stringify(cfg, null, 2)], {
    type: "application/json",
  });
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = "config.json";
  a.click();
  URL.revokeObjectURL(a.href);
});

$("#cfg-import")?.addEventListener("click", () => {
  const input = document.createElement("input");
  input.type = "file";
  input.accept = ".json,.jsonc";
  input.onchange = async () => {
    const file = input.files[0];
    if (!file) return;
    try {
      const text = await file.text();
      const cfg = JSON.parse(text);
      currentConfig = cfg;
      renderConfigForm(cfg);
      setViewMode("form");
      updateUnsavedDot();
      toast("Imported — save to apply", "success");
    } catch (e) {
      toast(`Import failed: ${e.message}`, "error");
    }
  };
  input.click();
});

// ---------------- zone editor ----------------

function openZoneEditor(camIdx) {
  const cam = currentConfig.cameras[camIdx];
  const zones = cam.motion?.zones || [];
  const zonesCopy = JSON.parse(JSON.stringify(zones));

  const overlay = el("div", { class: "modal-overlay" });
  const modal = el("div", { class: "modal zone-modal" });
  modal.appendChild(el("h2", { text: `Zones - ${cam.name}` }));
  modal.appendChild(
    el("p", {
      class: "modal-desc",
      text: "Click to add points. Click near the first point to close a zone. Right-click to undo last point.",
    }),
  );

  const canvasW = 640,
    canvasH = 360;
  const cvs = el("canvas", {
    width: canvasW,
    height: canvasH,
    class: "zone-canvas",
  });
  modal.appendChild(cvs);

  let currentPoints = [];
  const activeZones = zonesCopy;

  function draw() {
    const ctx = cvs.getContext("2d");
    ctx.fillStyle = "#0d1117";
    ctx.fillRect(0, 0, canvasW, canvasH);
    // grid
    ctx.strokeStyle = "#1a222b";
    ctx.lineWidth = 1;
    for (let x = 0; x < canvasW; x += 32) {
      ctx.beginPath();
      ctx.moveTo(x, 0);
      ctx.lineTo(x, canvasH);
      ctx.stroke();
    }
    for (let y = 0; y < canvasH; y += 32) {
      ctx.beginPath();
      ctx.moveTo(0, y);
      ctx.lineTo(canvasW, y);
      ctx.stroke();
    }
    // existing zones
    const colors = [
      "rgba(74,163,223,0.3)",
      "rgba(76,217,100,0.3)",
      "rgba(224,91,91,0.3)",
      "rgba(240,173,78,0.3)",
    ];
    const strokeColors = ["#4aa3df", "#4cd964", "#e05b5b", "#f0ad4e"];
    activeZones.forEach((z, zi) => {
      if (z.points.length < 3) return;
      ctx.fillStyle = colors[zi % colors.length];
      ctx.strokeStyle = strokeColors[zi % strokeColors.length];
      ctx.lineWidth = 2;
      ctx.beginPath();
      ctx.moveTo(z.points[0].x * canvasW, z.points[0].y * canvasH);
      z.points.forEach((pt) => {
        ctx.lineTo(pt.x * canvasW, pt.y * canvasH);
      });
      ctx.closePath();
      ctx.fill();
      ctx.stroke();
      // label
      ctx.fillStyle = strokeColors[zi % strokeColors.length];
      ctx.font = "12px system-ui";
      ctx.fillText(
        `Zone ${zi + 1}`,
        z.points[0].x * canvasW + 4,
        z.points[0].y * canvasH - 4,
      );
    });
    // current drawing zone
    if (currentPoints.length > 0) {
      ctx.strokeStyle = "#f0ad4e";
      ctx.lineWidth = 2;
      ctx.setLineDash([4, 4]);
      ctx.beginPath();
      ctx.moveTo(currentPoints[0].x * canvasW, currentPoints[0].y * canvasH);
      currentPoints.forEach((pt) => {
        ctx.lineTo(pt.x * canvasW, pt.y * canvasH);
      });
      ctx.stroke();
      ctx.setLineDash([]);
      currentPoints.forEach((pt) => {
        ctx.fillStyle = "#f0ad4e";
        ctx.beginPath();
        ctx.arc(pt.x * canvasW, pt.y * canvasH, 4, 0, Math.PI * 2);
        ctx.fill();
      });
    }
    // zone list
    zoneList.replaceChildren();
    activeZones.forEach((z, zi) => {
      const item = el(
        "div",
        { class: "zone-list-item" },
        el("span", { text: `Zone ${zi + 1} (${z.points.length} pts)` }),
        el("button", {
          class: "danger",
          text: "×",
          onclick: () => {
            activeZones.splice(zi, 1);
            draw();
          },
        }),
      );
      zoneList.appendChild(item);
    });
  }

  cvs.addEventListener("click", (e) => {
    const rect = cvs.getBoundingClientRect();
    const nx = (e.clientX - rect.left) / canvasW;
    const ny = (e.clientY - rect.top) / canvasH;
    // close zone if near first point
    if (currentPoints.length >= 3) {
      const dx = nx - currentPoints[0].x;
      const dy = ny - currentPoints[0].y;
      if (Math.sqrt(dx * dx + dy * dy) < 0.03) {
        activeZones.push({ points: [...currentPoints] });
        currentPoints = [];
        draw();
        return;
      }
    }
    currentPoints.push({
      x: Math.round(nx * 1000) / 1000,
      y: Math.round(ny * 1000) / 1000,
    });
    draw();
  });

  cvs.addEventListener("contextmenu", (e) => {
    e.preventDefault();
    if (currentPoints.length > 0) {
      currentPoints.pop();
      draw();
    }
  });

  const zoneList = el("div", { class: "zone-list" });
  modal.appendChild(zoneList);

  const btnRow = el("div", { class: "row", style: "margin-top:12px" });
  btnRow.appendChild(
    el("button", {
      text: "Apply",
      onclick: () => {
        if (!currentConfig.cameras[camIdx].motion)
          currentConfig.cameras[camIdx].motion = {};
        currentConfig.cameras[camIdx].motion.zones = activeZones;
        overlay.remove();
        renderConfigForm(currentConfig);
      },
    }),
  );
  btnRow.appendChild(
    el("button", {
      class: "secondary",
      text: "Cancel",
      onclick: () => overlay.remove(),
    }),
  );
  modal.appendChild(btnRow);

  overlay.appendChild(modal);
  overlay.addEventListener("click", (e) => {
    if (e.target === overlay) overlay.remove();
  });
  document.body.appendChild(overlay);
  draw();
}

// ---------------- mic calibration ----------------

async function runMicCalibration(camName) {
  const box = $(
    `#mic-cal-${currentConfig.cameras.findIndex((c) => c.name === camName)}`,
  );
  if (!box) return;
  box.textContent = "calibrating...";
  box.className = "mic-cal-result";
  try {
    const r = await api(
      `/api/mic-calibrate?cam=${encodeURIComponent(camName)}&dur=3`,
    );
    box.innerHTML = "";
    const row = (l, v) =>
      el(
        "div",
        { class: "mic-cal-row" },
        el("span", { text: l }),
        el("span", { class: "mono", text: String(v) }),
      );
    box.append(
      row("Noise floor", r.noise_floor),
      row("Peak", r.peak_level),
      row("P50", r.p50),
      row("P95", r.p95),
      row("Suggested gain", r.suggested_gain),
      row("Suggested gate", r.suggested_gate),
      el("button", {
        class: "secondary",
        text: "Apply suggested",
        style: "margin-top:6px",
        onclick: () => {
          const idx = currentConfig.cameras.findIndex(
            (c) => c.name === camName,
          );
          if (idx < 0) return;
          currentConfig.cameras[idx].mic.gain = r.suggested_gain;
          currentConfig.cameras[idx].mic.noise_gate = r.suggested_gate;
          renderConfigForm(currentConfig);
        },
      }),
    );
  } catch (e) {
    box.textContent = e.message;
    box.className = "mic-cal-result err";
  }
}

// ---------------- boot ----------------

document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") {
    const overlay = document.querySelector(".modal-overlay:last-of-type");
    if (overlay) overlay.remove();
  }
});

async function refresh() {
  const tab = document.querySelector(".tab.active")?.dataset.tab;
  try {
    if (tab === "status") await renderStatus();
  } catch (e) {
    toast(`Failed to refresh: ${e.message}`, "error");
  }
}

loadConfig();
setInterval(refresh, 15000);
refresh();
// Load data if deep-linked (after all variables are initialized).
if (location.hash === "#/recordings") renderRecordings();
if (location.hash === "#/logs") renderLogs();

// ---------------- SSE for live updates ----------------

let sseRetryDelay = 1000;
let lastSSEConfig = "";
function connectSSE() {
  const es = new EventSource("/api/events");
  es.addEventListener("config-changed", (e) => {
    sseRetryDelay = 1000;
    try {
      if (e.data === lastSSEConfig) return;
      lastSSEConfig = e.data;
      const cfg = JSON.parse(e.data);
      currentConfig = cfg;
      savedSnapshot = JSON.parse(JSON.stringify(cfg));
      const wasOpen = saveAccordionState();
      renderConfigForm(cfg);
      restoreAccordionState(wasOpen);
      updateUnsavedDot();
    } catch (_) {}
  });
  es.addEventListener("recordings-changed", () => {
    sseRetryDelay = 1000;
    // Invalidate cache so next render picks up changes.
    _lastRecTotal = -1;
    _lastRecModTime = "";
    const tab = document.querySelector(".tab.active")?.dataset.tab;
    if (tab === "recordings") {
      renderRecordings();
      toast("New recording available", "info");
    }
  });
  es.addEventListener("log-line", (e) => {
    sseRetryDelay = 1000;
    try {
      const evt = JSON.parse(e.data);
      const pre = $("#log-view");
      if (!pre) return;
      const sel = $("#log-name");
      if (!sel || sel.value !== evt.name) return;
      if (pre.textContent === "Loading\u2026") pre.textContent = "";
      // Only auto-scroll if user was already at the bottom.
      const nearBottom = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 40;
      pre.textContent += evt.line + "\n";
      if (nearBottom) pre.scrollTop = pre.scrollHeight;
    } catch (_) {}
  });
  es.onerror = () => {
    es.close();
    setTimeout(connectSSE, sseRetryDelay);
    sseRetryDelay = Math.min(sseRetryDelay * 2, 30000);
  };
}
connectSSE();
