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
  tabButtons.forEach((b) => b.classList.toggle("active", b === btn));
  document.querySelectorAll(".panel").forEach((p) => {
    p.classList.toggle("active", p.id === `tab-${btn.dataset.tab}`);
  });
  return true;
}
for (const btn of tabButtons) {
  btn.addEventListener("click", () => {
    activateTab(btn.dataset.tab);
    history.replaceState(null, "", `#/${btn.dataset.tab}`);
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
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

// ---------------- devices cache ----------------

let deviceCache = null;
async function getDevices() {
  if (!deviceCache) {
    try { deviceCache = await api("/api/devices"); } catch (_) { deviceCache = { video: [], audio: [], encoders: [] }; }
  }
  return deviceCache;
}
function invalidateDevices() { deviceCache = null; }

// ---------------- form helpers ----------------

function field(label, input, cls) {
  const row = el("div", { class: "field" + (cls ? " " + cls : "") });
  row.appendChild(el("label", { text: label }));
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
  return el("input", { type: "checkbox", "data-name": name, ...(val ? { checked: "checked" } : {}) });
}

function selectInput(val, name, opts) {
  const s = el("select", { "data-name": name });
  for (const o of opts) {
    const opt = el("option", { value: o.value, text: o.label });
    if (o.value === val) opt.selected = true;
    s.appendChild(opt);
  }
  return s;
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

function renderConfigForm(cfg) {
  currentConfig = cfg;
  const wrap = $("#cfg-form");
  wrap.replaceChildren();

  const globalSec = section("Global Settings",
    subsection("Recording",
      grid(
        field("Mode", selectInput(cfg.record?.mode ?? "event", "record.mode", [
          { value: "event", label: "Event (motion/sound)" },
          { value: "continuous", label: "Continuous" },
          { value: "combined", label: "Combined (tiled)" },
          { value: "none", label: "None (live only)" },
        ])),
        field("Retain hours", numInput(cfg.record?.retain_hours ?? 72, "record.retain_hours", { min: 0, size: "80px" })),
        field("Retain MB", numInput(cfg.record?.retain_mb ?? 0, "record.retain_mb", { min: 0, size: "80px" })),
      )
    ),
    subsection("Paths",
      grid(
        field("Record dir", textInput(cfg.paths?.record_dir ?? "", "paths.record_dir")),
        field("Log dir", textInput(cfg.paths?.log_dir ?? "", "paths.log_dir")),
        field("RTSP host", textInput(cfg.paths?.rtsp_host ?? "127.0.0.1", "paths.rtsp_host")),
      )
    ),
    subsection("Network",
      grid(
        field("Panel bind", textInput(cfg.web?.bind ?? "127.0.0.1", "web.bind")),
        field("Panel port", numInput(cfg.web?.port ?? 8080, "web.port", { min: 1, max: 65535, size: "80px" })),
        field("go2rtc API", numInput(cfg.go2rtc?.api_port ?? 1984, "go2rtc.api_port", { min: 1, max: 65535, size: "80px" })),
        field("RTSP port", numInput(cfg.go2rtc?.rtsp_port ?? 8554, "go2rtc.rtsp_port", { min: 1, max: 65535, size: "80px" })),
        field("WebRTC port", numInput(cfg.go2rtc?.webrtc_port ?? 8555, "go2rtc.webrtc_port", { min: 1, max: 65535, size: "80px" })),
      )
    ),
  );
  if (advancedMode) {
    globalSec.appendChild(subsection("GPU",
      grid(field("Encoders", textInput(cfg.gpu?.encoders ?? "auto", "gpu.encoders")))
    ));
    globalSec.appendChild(subsection("Logging",
      grid(
        field("Level", selectInput(cfg.log?.level ?? "info", "log.level", [
          { value: "debug", label: "Debug" }, { value: "info", label: "Info" }, { value: "warn", label: "Warn" },
        ])),
        field("Max MB", numInput(cfg.log?.max_mb ?? 10, "log.max_mb", { min: 1, size: "80px" })),
        field("Keep", numInput(cfg.log?.keep ?? 5, "log.keep", { min: 1, size: "80px" })),
      )
    ));
    globalSec.appendChild(subsection("Monitor",
      grid(
        field("Enabled", chkInput(cfg.monitor?.enabled ?? true, "monitor.enabled")),
        field("Interval (s)", numInput(cfg.monitor?.interval_sec ?? 10, "monitor.interval_sec", { min: 1, size: "80px" })),
        field("Alert after (s)", numInput(cfg.monitor?.alert_after_sec ?? 300, "monitor.alert_after_sec", { min: 0, size: "80px" })),
        field("ntfy topic", textInput(cfg.monitor?.ntfy_topic ?? "", "monitor.ntfy_topic")),
      )
    ));
  }
  wrap.appendChild(globalSec);

  const cams = cfg.cameras || [];
  for (let i = 0; i < cams.length; i++) {
    wrap.appendChild(renderCameraForm(cams[i], i));
  }

  const addBtn = el("button", { class: "secondary", text: "+ Add camera", onclick: showAddCameraModal });
  wrap.appendChild(addBtn);

  updateUnsavedDot();
}

function codecOpts(devices, isNvenc) {
  const opts = [{ value: "copy", label: "Copy (no re-encode)" }];
  if (devices?.nvenc) opts.push(
    { value: "h264_nvenc", label: "H.264 NVENC" },
    { value: "hevc_nvenc", label: "HEVC NVENC" },
    { value: "av1_nvenc", label: "AV1 NVENC" },
  );
  opts.push({ value: "libx264", label: "H.264 (CPU)" });
  return opts;
}

function renderCameraForm(cam, idx) {
  const p = (k) => `cameras.${idx}.${k}`;
  const prefix = cam.record?.prefix || cam.name || `cam${idx + 1}`;

  const camSec = el("details", { class: "cfg-camera" });
  camSec.appendChild(el("summary", {
    html: `<span class="cam-name">${esc(cam.name || "unnamed")}</span> <span class="cam-source">${esc(cam.source || "")}</span>`,
  }));
  const body = el("div", { class: "cfg-camera-body" });

  body.appendChild(el("button", {
    class: "danger remove-cam", text: "Remove camera",
    onclick: () => { currentConfig.cameras.splice(idx, 1); renderConfigForm(currentConfig); },
  }));

  const videoOpts = devices?.video?.length
    ? devices.video.map((d) => ({ value: d.name, label: d.name }))
    : [{ value: "", label: "(auto-detect)" }];

  body.appendChild(subsection("Basic",
    grid(
      field("Name", textInput(cam.name ?? "", p("name"))),
      field("Source", selectInput(cam.source ?? "dshow", p("source"), [
        { value: "dshow", label: "DirectShow (USB)" }, { value: "rtsp", label: "RTSP" },
      ])),
      field("Device", selectInput(cam.device_name ?? "", p("device_name"), videoOpts)),
    ),
    grid(
      field("Width", numInput(cam.width ?? 1920, p("width"), { min: 1, size: "80px" })),
      field("Height", numInput(cam.height ?? 1080, p("height"), { min: 1, size: "80px" })),
      field("FPS", numInput(cam.fps ?? 15, p("fps"), { min: 1, max: 60, size: "60px" })),
    ),
    cam.source === "rtsp" ? grid(
      field("RTSP URL", textInput(cam.rtsp_url ?? "", p("rtsp_url"), { size: "100%" })),
    ) : null,
  ));

  body.appendChild(subsection("Recording",
    grid(
      field("Enabled", chkInput(cam.record?.enabled ?? true, p("record.enabled"))),
      field("Prefix", textInput(prefix, p("record.prefix"))),
      field("Output dir", textInput(cam.record?.out_dir ?? "", p("record.out_dir"))),
    ),
    grid(
      field("Video codec", selectInput(cam.record?.video?.codec ?? "h264_nvenc", p("record.video.codec"), codecOpts(devices))),
      field("Audio codec", selectInput(cam.record?.audio?.codec ?? "aac", p("record.audio.codec"), [
        { value: "aac", label: "AAC" }, { value: "copy", label: "Copy" },
      ])),
      field("Audio bitrate", textInput(cam.record?.audio?.bitrate ?? "64k", p("record.audio.bitrate"), { size: "80px" })),
    ),
    grid(
      field("Pre-roll (s)", numInput(cam.record?.pre_roll_sec ?? 0, p("record.pre_roll_sec"), { min: 0, max: 60, size: "60px" })),
      field("Priority", numInput(cam.priority ?? 5, p("priority"), { min: 0, max: 10, size: "60px" })),
    ),
  ));

  if (advancedMode) {
    body.appendChild(subsection("Video Encoder",
      grid(
        field("Preset", textInput(cam.record?.video?.preset ?? "p2", p("record.video.preset"))),
        field("RC", textInput(cam.record?.video?.rc ?? "vbr", p("record.video.rc"))),
        field("CQ", numInput(cam.record?.video?.cq ?? 28, p("record.video.cq"), { min: 0, max: 51, size: "60px" })),
        field("Max rate", textInput(cam.record?.video?.maxrate ?? "4M", p("record.video.maxrate"), { size: "80px" })),
        field("GOP", numInput(cam.record?.video?.g ?? 15, p("record.video.g"), { min: 1, size: "60px" })),
      )
    ));
  }

  const audioOpts = devices?.audio?.length
    ? [{ value: "", label: "(auto-detect)" }, ...devices.audio.map((d) => ({ value: d.name, label: d.name }))]
    : [{ value: "", label: "(auto-detect)" }];

  const micCalBtn = el("button", {
    class: "secondary", type: "button", text: "Calibrate",
    onclick: () => runMicCalibration(cam.name),
  });
  const micCalRow = el("div", { class: "field" });
  micCalRow.appendChild(el("label", { text: "Calibrate" }));
  micCalRow.appendChild(micCalBtn);
  const micCalResult = el("div", { id: `mic-cal-${idx}`, class: "mic-cal-result" });

  body.appendChild(subsection("Microphone",
    grid(
      field("Enabled", chkInput(cam.mic?.enabled ?? false, p("mic.enabled"))),
      field("Device", selectInput(cam.mic?.device_name ?? "", p("mic.device_name"), audioOpts)),
    ),
    grid(
      field("Gain", numInput(cam.mic?.gain ?? 1, p("mic.gain"), { min: 0, step: 0.1, size: "60px" })),
      field("DC block", chkInput(cam.mic?.dc_block ?? true, p("mic.dc_block"))),
      field("Noise gate", numInput(cam.mic?.noise_gate ?? 0, p("mic.noise_gate"), { min: 0, size: "80px" })),
    ),
    advancedMode ? grid(
      field("Sample rate", numInput(cam.mic?.sample_rate ?? 48000, p("mic.sample_rate"), { min: 8000, size: "80px" })),
      field("Channels", numInput(cam.mic?.channels ?? 1, p("mic.channels"), { min: 1, max: 8, size: "60px" })),
      field("Backend", selectInput(cam.mic?.backend ?? "wasapi", p("mic.backend"), [
        { value: "wasapi", label: "WASAPI" }, { value: "auto", label: "Auto" },
      ])),
    ) : null,
    micCalRow,
    micCalResult,
  ));

  const zoneCount = (cam.motion?.zones || []).length;
  const zoneRow = el("div", { class: "field" });
  const zoneBtn = el("button", {
    class: "secondary", type: "button",
    text: zoneCount ? `Edit zones (${zoneCount})` : "Edit zones",
    onclick: () => openZoneEditor(idx),
  });
  zoneRow.appendChild(el("label", { text: "Zones" }));
  zoneRow.appendChild(zoneBtn);

  body.appendChild(subsection("Motion",
    grid(
      field("Enabled", chkInput(cam.motion?.enabled ?? true, p("motion.enabled"))),
      field("Detection", selectInput(cam.detection ?? "rawvideo", p("detection"), [
        { value: "rawvideo", label: "Raw (fast)" }, { value: "jpeg", label: "JPEG (MJPEG)" },
      ])),
    ),
    grid(
      field("Width", numInput(cam.motion?.width ?? 320, p("motion.width"), { min: 32, size: "60px" })),
      field("Height", numInput(cam.motion?.height ?? 180, p("motion.height"), { min: 32, size: "60px" })),
      field("FPS", numInput(cam.motion?.fps ?? 10, p("motion.fps"), { min: 1, max: 30, size: "60px" })),
    ),
    advancedMode ? grid(
      field("Threshold", numInput(cam.motion?.threshold ?? 25, p("motion.threshold"), { min: 1, size: "60px" })),
      field("Min pixels", numInput(cam.motion?.min_pixels ?? 250, p("motion.min_pixels"), { min: 1, size: "80px" })),
      field("Bg alpha", numInput(cam.motion?.bg_alpha ?? 0.05, p("motion.bg_alpha"), { min: 0, max: 1, step: 0.01, size: "60px" })),
      field("Post-roll (s)", numInput(cam.motion?.post_roll_sec ?? 3, p("motion.post_roll_sec"), { min: 0, step: 0.5, size: "60px" })),
      field("Event cap (s)", numInput(cam.motion?.event_cap_sec ?? 900, p("motion.event_cap_sec"), { min: 10, size: "80px" })),
      field("Restart backoff", numInput(cam.motion?.restart_backoff_sec ?? 2, p("motion.restart_backoff_sec"), { min: 0, step: 0.5, size: "60px" })),
    ) : null,
    zoneRow,
  ));

  if (advancedMode) {
    body.appendChild(subsection("Sound",
      grid(
        field("Enabled", chkInput(cam.sound?.enabled ?? true, p("sound.enabled"))),
        field("Abs floor", numInput(cam.sound?.abs_floor ?? 250, p("sound.abs_floor"), { min: 0, size: "80px" })),
        field("Ratio", numInput(cam.sound?.ratio ?? 3, p("sound.ratio"), { min: 0, step: 0.1, size: "60px" })),
      ),
      grid(
        field("Tau", numInput(cam.sound?.tau ?? 0.02, p("sound.tau"), { min: 0, step: 0.001, size: "60px" })),
        field("Hold blocks", numInput(cam.sound?.hold_blocks ?? 4, p("sound.hold_blocks"), { min: 0, size: "60px" })),
        field("Drop blocks", numInput(cam.sound?.drop_blocks ?? 30, p("sound.drop_blocks"), { min: 0, size: "60px" })),
      )
    ));
    body.appendChild(subsection("Image",
      grid(
        field("Brightness", numInput(cam.image?.brightness ?? 0, p("image.brightness"), { min: -1, max: 1, step: 0.1, size: "60px" })),
        field("Contrast", numInput(cam.image?.contrast ?? 0, p("image.contrast"), { min: -1, max: 1, step: 0.1, size: "60px" })),
        field("Saturation", numInput(cam.image?.saturation ?? 0, p("image.saturation"), { min: -1, max: 1, step: 0.1, size: "60px" })),
        field("Hue", numInput(cam.image?.hue ?? 0, p("image.hue"), { min: -1, max: 1, step: 0.1, size: "60px" })),
        field("Gamma", numInput(cam.image?.gamma ?? 0, p("image.gamma"), { min: -1, max: 1, step: 0.1, size: "60px" })),
      )
    ));
    body.appendChild(subsection("Live Stream",
      grid(
        field("Live codec", selectInput(cam.live?.video?.codec ?? "h264_nvenc", p("live.video.codec"), [
          { value: "h264_nvenc", label: "H.264 NVENC" }, { value: "copy", label: "Copy" },
        ])),
        field("Live audio", selectInput(cam.live?.audio?.codec ?? "aac", p("live.audio.codec"), [
          { value: "aac", label: "AAC" }, { value: "copy", label: "Copy" },
        ])),
        field("HLS", chkInput(cam.live?.hls ?? false, p("live.hls"))),
      )
    ));
  }

  camSec.appendChild(body);
  return camSec;
}

// ---------------- add camera modal (presets) ----------------

const PRESETS = {
  "USB 720p": {
    source: "dshow", width: 1280, height: 720, fps: 10,
    mic: { enabled: true, gain: 2, dc_block: true },
    motion: { enabled: true, width: 320, height: 180, fps: 10, threshold: 25, min_pixels: 250, bg_alpha: 0.05, post_roll_sec: 3, event_cap_sec: 900, restart_backoff_sec: 2 },
    sound: { enabled: true, abs_floor: 250, ratio: 3, tau: 0.02, hold_blocks: 4, drop_blocks: 30 },
    record: { enabled: true, video: { codec: "h264_nvenc", preset: "p2", rc: "vbr", cq: 28, maxrate: "4M", g: 15 }, audio: { codec: "aac", bitrate: "64k" } },
    priority: 5,
  },
  "USB 1080p": {
    source: "dshow", width: 1920, height: 1080, fps: 15,
    mic: { enabled: true, gain: 2, dc_block: true },
    motion: { enabled: true, width: 320, height: 180, fps: 10, threshold: 25, min_pixels: 250, bg_alpha: 0.05, post_roll_sec: 3, event_cap_sec: 900, restart_backoff_sec: 2 },
    sound: { enabled: true, abs_floor: 250, ratio: 3, tau: 0.02, hold_blocks: 4, drop_blocks: 30 },
    record: { enabled: true, video: { codec: "h264_nvenc", preset: "p2", rc: "vbr", cq: 28, maxrate: "4M", g: 15 }, audio: { codec: "aac", bitrate: "64k" } },
    priority: 5,
  },
  "RTSP": {
    source: "rtsp", width: 1920, height: 1080, fps: 15,
    mic: { enabled: false },
    motion: { enabled: true, width: 320, height: 180, fps: 10, threshold: 25, min_pixels: 250, bg_alpha: 0.05, post_roll_sec: 3, event_cap_sec: 900, restart_backoff_sec: 2 },
    sound: { enabled: false },
    record: { enabled: true, video: { codec: "copy", preset: "p2", rc: "vbr", cq: 28, maxrate: "4M", g: 15 }, audio: { codec: "copy", bitrate: "64k" } },
    priority: 5,
  },
  "RTSP + mic": {
    source: "rtsp", width: 1920, height: 1080, fps: 15,
    mic: { enabled: true, gain: 2, dc_block: true },
    motion: { enabled: true, width: 320, height: 180, fps: 10, threshold: 25, min_pixels: 250, bg_alpha: 0.05, post_roll_sec: 3, event_cap_sec: 900, restart_backoff_sec: 2 },
    sound: { enabled: true, abs_floor: 250, ratio: 3, tau: 0.02, hold_blocks: 4, drop_blocks: 30 },
    record: { enabled: true, video: { codec: "copy", preset: "p2", rc: "vbr", cq: 28, maxrate: "4M", g: 15 }, audio: { codec: "aac", bitrate: "64k" } },
    priority: 5,
  },
  "Blank": {
    source: "dshow", width: 1920, height: 1080, fps: 15,
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
  modal.appendChild(el("p", { class: "modal-desc", text: "Choose a preset or start blank:" }));

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
  modal.appendChild(el("button", { class: "secondary", text: "Cancel", style: "margin-top:12px", onclick: () => overlay.remove() }));
  overlay.appendChild(modal);
  overlay.addEventListener("click", (e) => { if (e.target === overlay) overlay.remove(); });
  document.body.appendChild(overlay);
}

function addCameraWithPreset(name, preset) {
  currentConfig.cameras = currentConfig.cameras || [];
  const idx = currentConfig.cameras.length;
  const camName = "cam" + (idx + 1);
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
    const parts = name.split(".");
    let obj = cfg;
    for (let i = 0; i < parts.length - 1; i++) {
      if (obj == null) break;
      const k = parts[i];
      const idx = /^\d+$/.test(k) ? parseInt(k) : null;
      if (idx !== null) obj = obj[idx];
      else obj = obj[k];
    }
    if (obj == null) continue;
    const key = parts[parts.length - 1];
    if (inp.type === "checkbox") obj[key] = inp.checked;
    else if (inp.type === "number") { const v = parseFloat(inp.value); if (!isNaN(v)) obj[key] = v; }
    else obj[key] = inp.value;
  }
  return cfg;
}

// ---------------- unsaved tracking ----------------

function updateUnsavedDot() {
  const dot = $("#unsaved-dot");
  if (!dot || !savedSnapshot) return;
  const current = JSON.stringify(collectForm());
  const saved = JSON.stringify(savedSnapshot);
  dot.hidden = current === saved;
}

function detectChanges() {
  if (!savedSnapshot) return false;
  return JSON.stringify(collectForm()) !== JSON.stringify(savedSnapshot);
}

// Listen for changes on form inputs (not keystrokes — #4f)
document.addEventListener("change", (e) => {
  if (e.target.closest("#cfg-form")) updateUnsavedDot();
}, true);

// ---------------- status ----------------

async function renderStatus() {
  const s = await api("/api/status");
  $("#live-link").href = `http://${location.hostname}:${s.live_port}`;
  const box = $("#tab-status");
  box.replaceChildren();
  const g = s.go2rtc;

  const camRows = (s.cameras || []).map((c) =>
    el("tr",
      el("td", { text: c.name }),
      el("td", { class: c.active ? "up" : "down", text: c.active ? "running" : "stopped" }),
      el("td", { text: c.mic ? "on" : "off" }),
      el("td", { text: c.mode }),
    ),
  );

  const sys = s.system || {};

  box.append(
    el("div", { class: "card" },
      el("h2", { text: "Service" }),
      el("table", {},
        el("tr", {}, el("td", { text: "Version" }), el("td", { text: s.version })),
        el("tr", {}, el("td", { text: "Uptime" }), el("td", { text: fmtDuration(s.uptime_ns) })),
        el("tr", {}, el("td", { text: "Config" }), el("td", { class: "mono", text: s.config_path })),
        el("tr", {}, el("td", { text: "Record mode" }), el("td", { text: s.mode })),
        el("tr", {}, el("td", { text: "go2rtc" }),
          el("td", {},
            el("span", { class: g.running ? "up" : "down", text: g.running ? "running" : "stopped" }),
            g.running ? ` (pid ${g.pid})` : "",
          ),
        ),
      ),
      adminControls(),
    ),
    el("div", { class: "card" },
      el("h2", { text: "Cameras" }),
      el("table", {},
        el("tr", {}, el("th", { text: "Name" }), el("th", { text: "Runner" }), el("th", { text: "Mic" }), el("th", { text: "Mode" })),
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
    rows.push(el("tr", {},
      el("td", { text: "Memory" }),
      el("td", { html: `<span class="${m.used_pct > 85 ? 'down' : ''}">${m.used_pct?.toFixed(0) ?? '?'}%</span> used / ${fmtSize(m.total ?? 0)}` }),
    ));
  }
  if (sys.disk) {
    const d = sys.disk;
    rows.push(el("tr", {},
      el("td", { text: "Disk" }),
      el("td", { html: `<span class="${d.used_pct > 90 ? 'down' : ''}">${d.used_pct?.toFixed(0) ?? '?'}%</span> used / ${fmtSize(d.total ?? 0)}` }),
    ));
  }
  if (!rows.length) return null;
  return el("div", { class: "card" },
    el("h2", { text: "System" }),
    el("table", {}, ...rows),
  );
}

// ---------------- admin ----------------

function adminControls() {
  const msg = el("span", { id: "svc-msg" });
  return el("div", { class: "row" },
    el("button", { class: "danger", text: "Restart service", onclick: restartService }),
    el("button", { class: "secondary", text: "Run retention", onclick: runRetention }),
    el("button", { class: "secondary", text: "Stop all", onclick: stopAll }),
    el("button", { class: "secondary", text: "Start all", onclick: startAll }),
    msg,
  );
}

async function restartService() {
  try {
    await api("/api/restart", { method: "POST" });
    $("#svc-msg").textContent = "restarting\u2026";
    $("#svc-msg").className = "up";
  } catch (e) {
    $("#svc-msg").textContent = e.message;
    $("#svc-msg").className = "err";
  }
}

async function runRetention() {
  try {
    await api("/api/retention/run", { method: "POST" });
    $("#svc-msg").textContent = "retention ran";
    $("#svc-msg").className = "up";
  } catch (e) {
    $("#svc-msg").textContent = e.message;
    $("#svc-msg").className = "err";
  }
}

async function stopAll() {
  try {
    await api("/api/stop-all", { method: "POST" });
    $("#svc-msg").textContent = "all cameras stopped";
    $("#svc-msg").className = "up";
    setTimeout(refresh, 1000);
  } catch (e) {
    $("#svc-msg").textContent = e.message;
    $("#svc-msg").className = "err";
  }
}

async function startAll() {
  try {
    await api("/api/start-all", { method: "POST" });
    $("#svc-msg").textContent = "all cameras started";
    $("#svc-msg").className = "up";
    setTimeout(refresh, 1000);
  } catch (e) {
    $("#svc-msg").textContent = e.message;
    $("#svc-msg").className = "err";
  }
}

// ---------------- logs ----------------

async function renderLogs() {
  const name = $("#log-name").value;
  const msg = $("#log-msg");
  try {
    const res = await fetch(`/api/logs?name=${encodeURIComponent(name)}&lines=1000`);
    if (!res.ok) {
      let data = await res.text();
      try { data = JSON.parse(data).error || data; } catch (_) {}
      throw new Error(data);
    }
    $("#log-view").textContent = await res.text();
    msg.textContent = "";
    msg.className = "";
  } catch (e) {
    msg.textContent = e.message;
    msg.className = "err";
  }
}
$("#log-refresh").addEventListener("click", renderLogs);

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
    box.append(
      el("div", { class: "card" },
        el("h2", { text: `${cam} (${recs.length})` }),
        el("table", {},
          el("tr", {}, el("th", { text: "File" }), el("th", { text: "Recorded" }), el("th", { text: "Size" })),
          ...recs.map((rec) =>
            el("tr", {},
              el("td", {}, el("a", { class: "play", onclick: () => play(rec) }, { text: rec.name })),
              el("td", { text: fmtTime(rec.mod_time) }),
              el("td", { text: fmtSize(rec.size) }),
            ),
          ),
        ),
      ),
    );
  }
}

function play(rec) {
  const player = $("#player");
  const src = "/api/recordings/" + encodeURIComponent(rec.camera) + "/" + encodeURIComponent(rec.name);
  if (player.dataset.src === src) { player.hidden = !player.hidden; return; }
  player.dataset.src = src;
  player.src = src;
  player.hidden = false;
  player.play().catch(() => {});
}

// ---------------- config ----------------

async function loadConfig() {
  $("#cfg-msg").textContent = "";
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
  for (const [k, v] of Object.entries(cfg.go2rtc || {})) {
    if (typeof v === "number" && v > 0) usedPorts.add(v);
  }
  if (typeof cfg.web?.port === "number") usedPorts.add(cfg.web.port);

  for (let i = 0; i < (cfg.cameras || []).length; i++) {
    const cam = cfg.cameras[i];
    const tag = `Camera ${i + 1} (${cam.name || "unnamed"})`;

    if (!cam.name || !cam.name.trim()) errors.push(`${tag}: name is empty.`);
    if (!cam.source) errors.push(`${tag}: source is required.`);
    if (cam.source === "rtsp" && !cam.rtsp_url) warnings.push(`${tag}: RTSP source has no URL.`);

    if (cam.source === "dshow" && !cam.device_name) {
      warnings.push(`${tag}: no device selected (will auto-detect).`);
    }

    if (cam.record?.enabled && (!cam.record.out_dir && !cfg.paths?.record_dir)) {
      warnings.push(`${tag}: recording enabled but no output dir set (will use ".").`);
    }

    if (cam.mic?.enabled && !cam.mic?.device_name) {
      warnings.push(`${tag}: mic enabled but no device (will auto-detect).`);
    }

    for (const p of [cam.mic?.live_port, cam.mic?.rec_port, cam.mic?.ctl_port]) {
      if (typeof p === "number" && p > 0) {
        if (usedPorts.has(p)) errors.push(`${tag}: port ${p} conflicts with another service.`);
        usedPorts.add(p);
      }
    }
  }

  if (cfg.record?.mode === "none" && cfg.cameras?.some((c) => c.record?.enabled)) {
    warnings.push("Global mode is 'none' but some cameras have recording enabled. Recording will be ignored.");
  }

  return { warnings, errors };
}

function showValidationDialog(warnings, errors) {
  return new Promise((resolve) => {
    const overlay = el("div", { class: "modal-overlay" });
    const modal = el("div", { class: "modal" });
    modal.appendChild(el("h2", { text: errors.length ? "Save blocked" : "Warnings" }));

    const list = el("div", { class: "validation-list" });
    for (const e of errors) list.appendChild(el("div", { class: "validation-item err", text: e }));
    for (const w of warnings) list.appendChild(el("div", { class: "validation-item", text: w }));
    modal.appendChild(list);

    const btnRow = el("div", { class: "row", style: "margin-top:12px" });
    if (errors.length === 0) {
      btnRow.appendChild(el("button", { text: "Save anyway", onclick: () => { overlay.remove(); resolve(true); } }));
      btnRow.appendChild(el("button", { class: "secondary", text: "Cancel", onclick: () => { overlay.remove(); resolve(false); } }));
    } else {
      btnRow.appendChild(el("button", { class: "secondary", text: "Fix issues", onclick: () => { overlay.remove(); resolve(false); } }));
    }
    modal.appendChild(btnRow);
    overlay.appendChild(modal);
    overlay.addEventListener("click", (e) => { if (e.target === overlay) { overlay.remove(); resolve(false); } });
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
      try { cfg = JSON.parse(text); } catch (e) { throw new Error("Invalid JSON: " + e.message); }
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
    $("#cfg-msg").textContent = "saved (reloads on next tick)";
    $("#cfg-msg").className = "up";
  } catch (e) {
    $("#cfg-msg").textContent = e.message;
    $("#cfg-msg").className = "err";
  }
});

$("#cfg-reload").addEventListener("click", loadConfig);

// ---------------- export / import ----------------

$("#cfg-export")?.addEventListener("click", () => {
  const cfg = collectForm();
  const blob = new Blob([JSON.stringify(cfg, null, 2)], { type: "application/json" });
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
      $("#cfg-msg").textContent = "imported (save to apply)";
      $("#cfg-msg").className = "up";
    } catch (e) {
      $("#cfg-msg").textContent = "import failed: " + e.message;
      $("#cfg-msg").className = "err";
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
  modal.appendChild(el("p", { class: "modal-desc", text: "Click to add points. Click near the first point to close a zone. Right-click to undo last point." }));

  const canvasW = 640, canvasH = 360;
  const cvs = el("canvas", { width: canvasW, height: canvasH, class: "zone-canvas" });
  modal.appendChild(cvs);

  let currentPoints = [];
  let activeZones = zonesCopy;

  function draw() {
    const ctx = cvs.getContext("2d");
    ctx.fillStyle = "#0d1117";
    ctx.fillRect(0, 0, canvasW, canvasH);
    // grid
    ctx.strokeStyle = "#1a222b";
    ctx.lineWidth = 1;
    for (let x = 0; x < canvasW; x += 32) { ctx.beginPath(); ctx.moveTo(x, 0); ctx.lineTo(x, canvasH); ctx.stroke(); }
    for (let y = 0; y < canvasH; y += 32) { ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(canvasW, y); ctx.stroke(); }
    // existing zones
    const colors = ["rgba(74,163,223,0.3)", "rgba(76,217,100,0.3)", "rgba(224,91,91,0.3)", "rgba(240,173,78,0.3)"];
    const strokeColors = ["#4aa3df", "#4cd964", "#e05b5b", "#f0ad4e"];
    activeZones.forEach((z, zi) => {
      if (z.points.length < 3) return;
      ctx.fillStyle = colors[zi % colors.length];
      ctx.strokeStyle = strokeColors[zi % strokeColors.length];
      ctx.lineWidth = 2;
      ctx.beginPath();
      ctx.moveTo(z.points[0].x * canvasW, z.points[0].y * canvasH);
      z.points.forEach((pt) => ctx.lineTo(pt.x * canvasW, pt.y * canvasH));
      ctx.closePath();
      ctx.fill();
      ctx.stroke();
      // label
      ctx.fillStyle = strokeColors[zi % strokeColors.length];
      ctx.font = "12px system-ui";
      ctx.fillText(`Zone ${zi + 1}`, z.points[0].x * canvasW + 4, z.points[0].y * canvasH - 4);
    });
    // current drawing zone
    if (currentPoints.length > 0) {
      ctx.strokeStyle = "#f0ad4e";
      ctx.lineWidth = 2;
      ctx.setLineDash([4, 4]);
      ctx.beginPath();
      ctx.moveTo(currentPoints[0].x * canvasW, currentPoints[0].y * canvasH);
      currentPoints.forEach((pt) => ctx.lineTo(pt.x * canvasW, pt.y * canvasH));
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
      const item = el("div", { class: "zone-list-item" },
        el("span", { text: `Zone ${zi + 1} (${z.points.length} pts)` }),
        el("button", { class: "danger", text: "×", onclick: () => { activeZones.splice(zi, 1); draw(); } }),
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
    currentPoints.push({ x: Math.round(nx * 1000) / 1000, y: Math.round(ny * 1000) / 1000 });
    draw();
  });

  cvs.addEventListener("contextmenu", (e) => {
    e.preventDefault();
    if (currentPoints.length > 0) { currentPoints.pop(); draw(); }
  });

  const zoneList = el("div", { class: "zone-list" });
  modal.appendChild(zoneList);

  const btnRow = el("div", { class: "row", style: "margin-top:12px" });
  btnRow.appendChild(el("button", {
    text: "Apply",
    onclick: () => {
      if (!currentConfig.cameras[camIdx].motion) currentConfig.cameras[camIdx].motion = {};
      currentConfig.cameras[camIdx].motion.zones = activeZones;
      overlay.remove();
      renderConfigForm(currentConfig);
    },
  }));
  btnRow.appendChild(el("button", { class: "secondary", text: "Cancel", onclick: () => overlay.remove() }));
  modal.appendChild(btnRow);

  overlay.appendChild(modal);
  overlay.addEventListener("click", (e) => { if (e.target === overlay) overlay.remove(); });
  document.body.appendChild(overlay);
  draw();
}

// ---------------- mic calibration ----------------

async function runMicCalibration(camName) {
  const box = $(`#mic-cal-${currentConfig.cameras.findIndex((c) => c.name === camName)}`);
  if (!box) return;
  box.textContent = "calibrating...";
  box.className = "mic-cal-result";
  try {
    const r = await api(`/api/mic-calibrate?cam=${encodeURIComponent(camName)}&dur=3`);
    box.innerHTML = "";
    const row = (l, v) => el("div", { class: "mic-cal-row" }, el("span", { text: l }), el("span", { class: "mono", text: String(v) }));
    box.append(
      row("Noise floor", r.noise_floor),
      row("Peak", r.peak_level),
      row("P50", r.p50),
      row("P95", r.p95),
      row("Suggested gain", r.suggested_gain),
      row("Suggested gate", r.suggested_gate),
      el("button", {
        class: "secondary", text: "Apply suggested", style: "margin-top:6px",
        onclick: () => {
          const idx = currentConfig.cameras.findIndex((c) => c.name === camName);
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

async function refresh() {
  const tab = document.querySelector(".tab.active")?.dataset.tab;
  try {
    if (tab === "status") await renderStatus();
    if (tab === "recordings") await renderRecordings();
    if (tab === "logs") await renderLogs();
  } catch (e) {
    const panel = document.querySelector(".panel.active");
    if (panel) panel.innerHTML = `<div class="card err">${esc(e.message)}</div>`;
  }
}

loadConfig();
setInterval(refresh, 5000);
refresh();
