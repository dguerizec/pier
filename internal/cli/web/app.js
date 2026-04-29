// pier dashboard — single-file SPA. Hash router, fetch wrapper around
// /api/v1/*, EventSource feed for live state. No build step, no deps.

const view = document.getElementById("view");
const connDot = document.getElementById("conn");
const versionEl = document.getElementById("version");
const tldEl = document.getElementById("tld");

const store = {
  workloads: [], // apiWorkload[]
  config: null,
  doctor: null,
};

// Light DOM helpers — text-only children to avoid any HTML injection path.
function el(tag, props = {}, children = []) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === "class") e.className = v;
    else if (k.startsWith("on")) e.addEventListener(k.slice(2).toLowerCase(), v);
    else if (v === false || v == null) continue;
    else if (v === true) e.setAttribute(k, "");
    else e.setAttribute(k, v);
  }
  for (const c of [].concat(children)) {
    if (c == null || c === false) continue;
    e.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
  }
  return e;
}

function fmtUptime(seconds) {
  if (!Number.isFinite(seconds) || seconds < 0) return "—";
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m`;
  return `${Math.floor(seconds)}s`;
}

async function api(path, opts = {}) {
  const init = { ...opts };
  if (init.body && typeof init.body !== "string") {
    init.headers = { "Content-Type": "application/json", ...(init.headers || {}) };
    init.body = JSON.stringify(init.body);
  }
  const r = await fetch(path, init);
  const ct = r.headers.get("Content-Type") || "";
  const data = ct.includes("application/json") ? await r.json() : await r.text();
  if (!r.ok) {
    const msg = (data && data.error) || `HTTP ${r.status}`;
    const e = new Error(msg);
    e.status = r.status;
    e.body = data;
    throw e;
  }
  return data;
}

// SSE: connect once on load, reconnect on close with exponential backoff.
function startEvents() {
  let backoff = 1000;
  function connect() {
    const es = new EventSource("/api/v1/events");
    es.addEventListener("open", () => {
      connDot.classList.add("live");
      connDot.classList.remove("dead");
      backoff = 1000;
    });
    es.addEventListener("error", () => {
      connDot.classList.remove("live");
      connDot.classList.add("dead");
      es.close();
      setTimeout(connect, backoff);
      backoff = Math.min(backoff * 2, 15000);
    });
    es.addEventListener("state.snapshot", (e) => {
      store.workloads = JSON.parse(e.data);
      render();
    });
    const upsert = (e) => {
      const wl = JSON.parse(e.data);
      const i = store.workloads.findIndex(
        (w) => w.project === wl.project && w.slug === wl.slug,
      );
      if (i >= 0) store.workloads[i] = wl;
      else store.workloads.push(wl);
      render();
    };
    es.addEventListener("workload.up", upsert);
    es.addEventListener("workload.crashed", upsert);
    es.addEventListener("workload.restarting", upsert);
    es.addEventListener("workload.removed", (e) => {
      const { project, slug } = JSON.parse(e.data);
      store.workloads = store.workloads.filter(
        (w) => !(w.project === project && w.slug === slug),
      );
      render();
    });
    es.addEventListener("doctor.fail", () => loadDoctor().then(render).catch(() => {}));
    es.addEventListener("doctor.recovered", () => loadDoctor().then(render).catch(() => {}));
  }
  connect();
}

async function loadConfig() {
  store.config = await api("/api/v1/config");
  if (store.config) {
    versionEl.textContent = store.config.version;
    tldEl.textContent = store.config.tld;
  }
}
async function loadDoctor() {
  store.doctor = await api("/api/v1/doctor");
}

// Hash router. Routes:
//   #/                                    → workloads list
//   #/workload/:project/:slug             → workload detail
//   #/workload/:project/:slug/logs        → log viewer
//   #/worktrees                           → worktrees panel
//   #/doctor                              → doctor checks
//   #/config                              → config readout
function parseRoute() {
  const h = (location.hash || "#/").replace(/^#/, "");
  const parts = h.split("/").filter(Boolean);
  if (parts.length === 0) return { name: "workloads" };
  if (parts[0] === "workload" && parts.length >= 3) {
    return {
      name: parts[3] === "logs" ? "logs" : "workload",
      project: decodeURIComponent(parts[1]),
      slug: decodeURIComponent(parts[2]),
    };
  }
  if (parts[0] === "worktrees") return { name: "worktrees" };
  if (parts[0] === "doctor") return { name: "doctor" };
  if (parts[0] === "config") return { name: "config" };
  return { name: "workloads" };
}

function syncNav(route) {
  const top =
    route.name === "workload" || route.name === "logs"
      ? "workloads"
      : route.name;
  for (const a of document.querySelectorAll("nav a[data-route]")) {
    a.classList.toggle("active", a.dataset.route === top);
  }
}

let logCleanup = null;
async function render() {
  if (logCleanup) { logCleanup(); logCleanup = null; }
  const route = parseRoute();
  syncNav(route);
  view.replaceChildren();

  switch (route.name) {
    case "workloads": return renderWorkloads();
    case "workload": return renderWorkloadDetail(route.project, route.slug);
    case "logs":     return renderLogs(route.project, route.slug);
    case "worktrees":return renderWorktrees();
    case "doctor":   return renderDoctor();
    case "config":   return renderConfig();
  }
}

// ----- views -----

function workloadByKey(project, slug) {
  return store.workloads.find((w) => w.project === project && w.slug === slug);
}

function urlList(urls) {
  if (!urls || urls.length === 0) return null;
  return el("div", { class: "urls" }, [
    el("div", { class: "label" }, "URLs"),
    ...urls.map((u) =>
      el("a", { href: u.url, target: "_blank", rel: "noopener", class: u.default ? "default" : "" }, u.label),
    ),
  ]);
}

function containerList(containers) {
  if (!containers || containers.length === 0) return null;
  return el("div", { class: "containers" }, [
    el("div", { class: "label" }, "Containers"),
    ...containers.map((c) =>
      el("div", { class: "container" }, [
        el("span", { class: `dot ${c.status}` }),
        el("span", { class: "name" }, c.name),
        el("span", { class: "image" }, c.image),
      ]),
    ),
  ]);
}

function renderWorkloads() {
  if (store.workloads.length === 0) {
    view.appendChild(
      el("div", { class: "empty" }, [
        "No workloads running. Try ",
        el("code", {}, "pier up"),
        " in a worktree.",
      ]),
    );
    return;
  }
  const groups = new Map();
  for (const w of store.workloads) {
    if (!groups.has(w.project)) groups.set(w.project, []);
    groups.get(w.project).push(w);
  }
  const projects = [...groups.entries()].sort(([a], [b]) => a.localeCompare(b));
  for (const [name, ws] of projects) {
    ws.sort((a, b) => a.slug.localeCompare(b.slug));
    const card = el("section", { class: "card" }, [
      el("header", {}, el("h2", {}, name)),
    ]);
    for (const w of ws) {
      card.appendChild(renderWorkloadRow(w));
    }
    view.appendChild(card);
  }
}

function renderWorkloadRow(w) {
  const detailHash = `#/workload/${encodeURIComponent(w.project)}/${encodeURIComponent(w.slug)}`;
  return el("div", { class: "workload" }, [
    el("div", { class: "ident" }, [
      el("span", { class: "slug" }, el("a", { href: detailHash }, w.slug)),
      el("span", { class: "branch" }, w.branch),
      el("span", { class: "status" }, [
        el("span", { class: `dot ${w.status}` }),
        `${w.status} · ${fmtUptime(w.uptime_seconds)}`,
      ]),
    ]),
    el("div", { class: "panels" }, [
      urlList(w.urls),
      containerList(w.containers),
      w.error ? el("div", { class: "err" }, "! " + w.error) : null,
    ]),
    el("div", { class: "actions" }, [
      el("a", { class: "btn", href: detailHash }, "details"),
    ]),
  ]);
}

async function renderWorkloadDetail(project, slug) {
  const cached = workloadByKey(project, slug);
  let w = cached;
  if (!w) {
    try {
      w = await api(`/api/v1/workloads/${encodeURIComponent(project)}/${encodeURIComponent(slug)}`);
    } catch (e) {
      view.appendChild(notFoundCard(project, slug, e.message));
      return;
    }
  }
  const back = el("div", { class: "crumbs" }, [
    el("a", { href: "#/" }, "← workloads"),
  ]);
  const running = w.status === "running";
  const logsHash = `#/workload/${encodeURIComponent(project)}/${encodeURIComponent(slug)}/logs`;

  const flash = el("div");
  const card = el("section", { class: "card" }, [
    el("header", {}, [
      el("h2", {}, `${project} / ${slug}`),
      el("div", { class: "toolbar" }, [
        el("a", { class: "btn", href: logsHash }, "logs"),
        running
          ? el("button", { class: "danger", onclick: () => doDown(project, slug, flash) }, "down")
          : el("button", { class: "primary", onclick: () => doUp(project, slug, flash) }, "up"),
      ]),
    ]),
    el("div", { class: "workload" }, [
      el("div", { class: "ident" }, [
        el("span", { class: "slug" }, w.slug),
        el("span", { class: "branch" }, w.branch),
        el("span", { class: "status" }, [
          el("span", { class: `dot ${w.status}` }),
          `${w.status} · ${fmtUptime(w.uptime_seconds)}`,
        ]),
      ]),
      el("div", { class: "panels" }, [
        urlList(w.urls),
        containerList(w.containers),
        w.error ? el("div", { class: "err" }, "! " + w.error) : null,
      ]),
      el("div"),
    ]),
    el("table", { class: "kv" }, [
      el("tr", {}, [el("td", {}, "worktree"), el("td", {}, w.worktree_path || "—")]),
      el("tr", {}, [el("td", {}, "kind"), el("td", {}, w.kind || "—")]),
      el("tr", {}, [el("td", {}, "started"), el("td", {}, w.started_at ? new Date(w.started_at).toLocaleString() : "—")]),
      el("tr", {}, [el("td", {}, "container id"), el("td", {}, w.container_id || "—")]),
    ]),
  ]);
  view.append(back, flash, card);
}

function notFoundCard(project, slug, msg) {
  return el("section", { class: "card" }, [
    el("header", {}, el("h2", {}, `${project} / ${slug}`)),
    el("div", { class: "flash error", style: "margin: 1rem;" }, msg || "not found"),
  ]);
}

async function doUp(project, slug, flash) {
  flash.replaceChildren(el("div", { class: "flash" }, "starting…"));
  try {
    const wl = await api(
      `/api/v1/workloads/${encodeURIComponent(project)}/${encodeURIComponent(slug)}/up`,
      { method: "POST" },
    );
    flash.replaceChildren(el("div", { class: "flash success" }, `up — status ${wl.status}`));
    const i = store.workloads.findIndex((w) => w.project === project && w.slug === slug);
    if (i >= 0) store.workloads[i] = wl;
    render();
  } catch (e) {
    flash.replaceChildren(el("div", { class: "flash error" }, "up failed: " + e.message));
  }
}

async function doDown(project, slug, flash) {
  flash.replaceChildren(el("div", { class: "flash" }, "stopping…"));
  try {
    const r = await api(
      `/api/v1/workloads/${encodeURIComponent(project)}/${encodeURIComponent(slug)}/down`,
      { method: "POST" },
    );
    flash.replaceChildren(el("div", { class: "flash success" }, r.warning ? r.warning : "down"));
    store.workloads = store.workloads.filter((w) => !(w.project === project && w.slug === slug));
    render();
  } catch (e) {
    flash.replaceChildren(el("div", { class: "flash error" }, "down failed: " + e.message));
  }
}

// Logs viewer streams the chunked /logs endpoint into a <pre>. The fetch
// reader is canceled when the user navigates away (logCleanup) so the
// underlying docker logs subprocess gets killed via r.Context().
async function renderLogs(project, slug) {
  const back = el("div", { class: "crumbs" }, [
    el("a", { href: `#/workload/${encodeURIComponent(project)}/${encodeURIComponent(slug)}` },
      `← ${project} / ${slug}`),
  ]);
  const pre = el("pre", { class: "logs" }, "connecting…");
  const card = el("section", { class: "card" }, [
    el("header", {}, [
      el("h2", {}, `${project} / ${slug} — logs`),
      el("div", { class: "toolbar" }, [
        el("button", { onclick: () => { pre.textContent = ""; } }, "clear"),
      ]),
    ]),
    el("div", { style: "padding: 1rem;" }, pre),
  ]);
  view.append(back, card);

  const ctrl = new AbortController();
  logCleanup = () => ctrl.abort();
  try {
    const r = await fetch(
      `/api/v1/workloads/${encodeURIComponent(project)}/${encodeURIComponent(slug)}/logs?follow=true&tail=200`,
      { signal: ctrl.signal },
    );
    if (!r.ok) {
      pre.textContent = `error: HTTP ${r.status}`;
      return;
    }
    pre.textContent = "";
    const reader = r.body.getReader();
    const dec = new TextDecoder();
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      pre.appendChild(document.createTextNode(dec.decode(value, { stream: true })));
      pre.scrollTop = pre.scrollHeight;
    }
  } catch (e) {
    if (e.name !== "AbortError") {
      pre.appendChild(document.createTextNode(`\n[stream error: ${e.message}]`));
    }
  }
}

// Pier has no central worktree registry — the workload list is the best
// proxy. We surface unique (project, worktree_path) pairs so the user
// can see and delete them, and provide a create form.
function renderWorktrees() {
  const seen = new Map();
  for (const w of store.workloads) {
    const key = w.worktree_path || `${w.project}/${w.slug}`;
    if (!seen.has(key)) seen.set(key, w);
  }
  const rows = [...seen.values()];

  view.appendChild(renderWorktreeForm());

  const card = el("section", { class: "card" }, [
    el("header", {}, el("h2", {}, "active worktrees")),
  ]);
  if (rows.length === 0) {
    card.appendChild(
      el("div", { class: "empty", style: "border: none; border-radius: 0;" },
        "no active worktrees — create one above or run `pier up` in a checkout"),
    );
  } else {
    rows.sort((a, b) => (a.worktree_path || "").localeCompare(b.worktree_path || ""));
    for (const w of rows) {
      card.appendChild(renderWorktreeRow(w));
    }
  }
  view.appendChild(card);
}

function renderWorktreeForm() {
  const flash = el("div");
  const form = el("form", {
    class: "stack",
    onsubmit: async (e) => {
      e.preventDefault();
      const data = Object.fromEntries(new FormData(e.target));
      data.up = !!data.up;
      flash.replaceChildren(el("div", { class: "flash" }, "creating…"));
      try {
        const r = await api("/api/v1/worktrees", { method: "POST", body: data });
        flash.replaceChildren(
          el("div", { class: "flash success" },
            `created at ${r.worktree_path} (branch ${r.branch})${r.workload ? " · workload up" : ""}`),
        );
        e.target.reset();
        if (r.workload) {
          const i = store.workloads.findIndex(
            (w) => w.project === r.workload.project && w.slug === r.workload.slug,
          );
          if (i >= 0) store.workloads[i] = r.workload;
          else store.workloads.push(r.workload);
          render();
        }
      } catch (err) {
        flash.replaceChildren(el("div", { class: "flash error" }, "create failed: " + err.message));
      }
    },
  }, [
    el("label", {}, [
      el("span", {}, "repo (absolute path of primary worktree)"),
      el("input", { type: "text", name: "repo", required: true, placeholder: "/home/you/dev/myproject" }),
    ]),
    el("label", {}, [
      el("span", {}, "slug (directory name + workload slug)"),
      el("input", { type: "text", name: "slug", required: true, placeholder: "feat-new-thing" }),
    ]),
    el("label", {}, [
      el("span", {}, "branch (optional, defaults to slug)"),
      el("input", { type: "text", name: "branch", placeholder: "feat/new-thing" }),
    ]),
    el("label", {}, [
      el("span", {}, "from (optional base ref)"),
      el("input", { type: "text", name: "from", placeholder: "main" }),
    ]),
    el("label", { class: "row" }, [
      el("input", { type: "checkbox", name: "up", value: "true" }),
      el("span", {}, "pier up after create"),
    ]),
    el("div", { class: "row" }, [
      el("button", { class: "primary", type: "submit" }, "create worktree"),
      el("span", { class: "spacer" }),
    ]),
    flash,
  ]);
  return el("section", { class: "card" }, [
    el("header", {}, el("h2", {}, "new worktree")),
    form,
  ]);
}

function renderWorktreeRow(w) {
  const flash = el("div", { style: "padding: 0 1rem 0.85rem;" });
  return el("div", {}, [
    el("div", { class: "workload" }, [
      el("div", { class: "ident" }, [
        el("span", { class: "slug" }, w.slug),
        el("span", { class: "branch" }, w.branch),
        el("span", { class: "status" }, [
          el("span", { class: `dot ${w.status}` }),
          w.status,
        ]),
      ]),
      el("div", { class: "panels" }, [
        el("div", { class: "label" }, "path"),
        el("div", { style: "font-family: var(--mono); font-size: 0.82rem; word-break: break-all;" },
          w.worktree_path || "—"),
      ]),
      el("div", { class: "actions" }, [
        el("button", {
          class: "danger",
          onclick: () => deleteWorktree(w, flash),
        }, "delete"),
      ]),
    ]),
    flash,
  ]);
}

// The DELETE endpoint needs ?repo=<absolute primary worktree path>, but
// the workload only knows its own (secondary) worktree path. Pier has no
// repo registry, so prompt the user.
async function deleteWorktree(w, flash) {
  const repo = prompt(
    `Delete worktree "${w.slug}" (${w.worktree_path}).\n\n` +
    `Provide the absolute path of the PRIMARY worktree (the repo) — pier needs it to know which checkout to remove from.`,
    "",
  );
  if (!repo) return;
  flash.replaceChildren(el("div", { class: "flash" }, "deleting…"));
  try {
    const r = await api(
      `/api/v1/worktrees/${encodeURIComponent(w.slug)}?repo=${encodeURIComponent(repo)}`,
      { method: "DELETE" },
    );
    flash.replaceChildren(
      el("div", { class: r.warning ? "flash warn" : "flash success" },
        r.warning || `removed ${w.slug}`),
    );
    store.workloads = store.workloads.filter(
      (x) => !(x.project === w.project && x.slug === w.slug),
    );
    render();
  } catch (e) {
    flash.replaceChildren(el("div", { class: "flash error" }, "delete failed: " + e.message));
  }
}

async function renderDoctor() {
  if (!store.doctor) {
    try { await loadDoctor(); }
    catch (e) {
      view.appendChild(el("div", { class: "flash error" }, "doctor load failed: " + e.message));
      return;
    }
  }
  const r = store.doctor;
  view.appendChild(
    el("div", { class: r.failed ? "flash error" : "flash success" },
      r.failed ? "checks failing — see below" : "all checks passing"),
  );
  const ul = el("ul", { class: "checks" });
  for (const c of r.checks) {
    ul.appendChild(
      el("li", {}, [
        el("span", { class: `dot ${c.status}` }),
        el("div", { class: "name" }, c.name),
        c.detail ? el("div", { class: "detail" }, c.detail) : null,
        c.fix_hint ? el("div", { class: "fix" }, "fix: " + c.fix_hint) : null,
      ]),
    );
  }
  view.appendChild(
    el("section", { class: "card" }, [
      el("header", {}, [
        el("h2", {}, "checks"),
        el("button", {
          onclick: async () => { await loadDoctor(); render(); },
        }, "refresh"),
      ]),
      ul,
    ]),
  );
}

async function renderConfig() {
  if (!store.config) {
    try { await loadConfig(); }
    catch (e) {
      view.appendChild(el("div", { class: "flash error" }, "config load failed: " + e.message));
      return;
    }
  }
  const c = store.config;
  const rows = [
    ["mode", c.mode],
    ["tld", c.tld],
    ["bind ip", c.bind_ip],
    ["answer ip", c.answer_ip],
    ["traefik network", c.traefik_network],
    ["external traefik", c.external_traefik || "—"],
    ["headscale records", c.headscale_records_path || "—"],
    ["version", c.version],
  ];
  view.appendChild(
    el("section", { class: "card" }, [
      el("header", {}, el("h2", {}, "config")),
      el("table", { class: "kv" },
        rows.map(([k, v]) => el("tr", {}, [el("td", {}, k), el("td", {}, String(v))])),
      ),
    ]),
  );
}

// ----- bootstrap -----

window.addEventListener("hashchange", render);
loadConfig().catch(() => {});
startEvents();
