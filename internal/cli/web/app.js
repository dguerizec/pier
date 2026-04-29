// pier dashboard — single-file SPA. Hash router, fetch wrapper around
// /api/v1/*, EventSource feed for live state. No build step, no deps.

const view = document.getElementById("view");
const connDot = document.getElementById("conn");
const versionEl = document.getElementById("version");
const tldEl = document.getElementById("tld");

const store = {
  workloads: [],         // apiWorkload[] from SSE
  config: null,
  doctor: null,
  projects: null,        // apiProjectListItem[] (lazy-loaded)
  projectDetail: {},     // name -> apiProject (lazy-loaded)
  projectWorktrees: {},  // name -> apiWorktreeListItem[]
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
async function loadProjects() {
  store.projects = await api("/api/v1/projects");
}
async function loadProjectDetail(name) {
  store.projectDetail[name] = await api(`/api/v1/projects/${encodeURIComponent(name)}`);
}
async function loadProjectWorktrees(name) {
  store.projectWorktrees[name] = await api(
    `/api/v1/projects/${encodeURIComponent(name)}/worktrees`,
  );
}

// Hash router. Routes:
//   #/                                          dashboard (workloads list)
//   #/projects                                  projects list + register form
//   #/projects/:name                            project detail + worktrees
//   #/projects/:name/workload/:slug             workload detail
//   #/projects/:name/workload/:slug/logs        log viewer
//   #/doctor
//   #/config
function parseRoute() {
  const h = (location.hash || "#/").replace(/^#/, "");
  const parts = h.split("/").filter(Boolean);
  if (parts.length === 0) return { name: "dashboard" };
  if (parts[0] === "projects") {
    if (parts.length === 1) return { name: "projects" };
    const project = decodeURIComponent(parts[1]);
    if (parts.length === 2) return { name: "project", project };
    if (parts[2] === "workload" && parts.length >= 4) {
      const slug = decodeURIComponent(parts[3]);
      return {
        name: parts[4] === "logs" ? "logs" : "workload",
        project,
        slug,
      };
    }
    return { name: "project", project };
  }
  if (parts[0] === "doctor") return { name: "doctor" };
  if (parts[0] === "config") return { name: "config" };
  return { name: "dashboard" };
}

function syncNav(route) {
  let top = route.name;
  if (top === "project" || top === "workload" || top === "logs") top = "projects";
  if (top === "dashboard") top = "dashboard";
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
    case "dashboard": return renderDashboard();
    case "projects":  return renderProjectsList();
    case "project":   return renderProjectDetail(route.project);
    case "workload":  return renderWorkloadDetail(route.project, route.slug);
    case "logs":      return renderLogs(route.project, route.slug);
    case "doctor":    return renderDoctor();
    case "config":    return renderConfig();
  }
}

// ----- shared row pieces -----

function workloadByKey(project, slug) {
  return store.workloads.find((w) => w.project === project && w.slug === slug);
}

function workloadDetailHash(project, slug) {
  return `#/projects/${encodeURIComponent(project)}/workload/${encodeURIComponent(slug)}`;
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

function renderWorkloadRow(w) {
  const detailHash = workloadDetailHash(w.project, w.slug);
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

// ----- dashboard (workloads, current behavior) -----

function renderDashboard() {
  if (store.workloads.length === 0) {
    view.appendChild(
      el("div", { class: "empty" }, [
        "No running workloads. Try ",
        el("code", {}, "pier up"),
        " in a worktree, or open a project from ",
        el("a", { href: "#/projects" }, "projects"),
        ".",
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
      el("header", {}, [
        el("h2", {}, el("a", { href: `#/projects/${encodeURIComponent(name)}`, style: "color: inherit; text-decoration: none;" }, name)),
        el("a", { class: "btn", href: `#/projects/${encodeURIComponent(name)}` }, "open project"),
      ]),
    ]);
    for (const w of ws) {
      card.appendChild(renderWorkloadRow(w));
    }
    view.appendChild(card);
  }
}

// ----- projects list -----

async function renderProjectsList() {
  const flash = el("div");
  view.appendChild(flash);
  view.appendChild(renderInitWizard(flash));

  if (!store.projects) {
    try { await loadProjects(); }
    catch (e) {
      view.appendChild(el("div", { class: "flash error" }, "load projects failed: " + e.message));
      return;
    }
  }

  const card = el("section", { class: "card" }, [
    el("header", {}, [
      el("h2", {}, "registered projects"),
      el("button", {
        onclick: async () => { await loadProjects(); render(); },
      }, "refresh"),
    ]),
  ]);
  if (!store.projects || store.projects.length === 0) {
    card.appendChild(
      el("div", { class: "empty", style: "border: none; border-radius: 0;" }, [
        "No projects registered yet. Run ",
        el("code", {}, "pier init"),
        " in a repo, or use the wizard above.",
      ]),
    );
  } else {
    for (const p of store.projects) {
      card.appendChild(renderProjectRow(p));
    }
  }
  view.appendChild(card);
}

function renderProjectRow(p) {
  const projectHash = `#/projects/${encodeURIComponent(p.name)}`;
  return el("div", { class: "workload" }, [
    el("div", { class: "ident" }, [
      el("span", { class: "slug" }, el("a", { href: projectHash }, p.name)),
      el("span", { class: "branch" }, p.repo_path),
      el("span", { class: "status" }, [
        el("span", { class: `dot ${p.has_manifest ? "running" : "warn"}` }),
        p.has_manifest
          ? `${p.active_workloads} active workload${p.active_workloads === 1 ? "" : "s"}`
          : "manifest missing",
      ]),
    ]),
    el("div", { class: "panels" }, []),
    el("div", { class: "actions" }, [
      el("a", { class: "btn", href: projectHash }, "open"),
    ]),
  ]);
}

// ----- init wizard (scan + edit + create) -----
//
// Two-step inline flow embedded into the projects view: enter a repo path,
// click scan, the form expands with the suggested manifest pre-filled,
// edit if needed, then submit. Hides the wizard's complexity behind
// "accept defaults and click create" for the common case.
function renderInitWizard(parentFlash) {
  let scanState = null; // { repo, suggested, services, isReinit }
  const expanded = el("div");

  const scanBtn = el("button", { class: "primary", type: "submit" }, "scan");
  const repoInput = el("input", {
    type: "text", name: "repo", required: true,
    placeholder: "/home/you/dev/myproject",
  });
  const scanForm = el("form", {
    onsubmit: async (e) => {
      e.preventDefault();
      scanBtn.disabled = true;
      scanBtn.textContent = "scanning…";
      expanded.replaceChildren();
      try {
        const r = await api("/api/v1/projects/scan", {
          method: "POST",
          body: { repo: repoInput.value },
        });
        scanState = {
          repo: r.toplevel,
          suggested: r.suggested_manifest || r.existing_manifest || emptyManifest(),
          services: r.services || [],
          isReinit: r.is_reinit,
        };
        expanded.appendChild(renderManifestForm(scanState, parentFlash));
      } catch (err) {
        expanded.appendChild(el("div", { class: "flash error" }, "scan failed: " + err.message));
      } finally {
        scanBtn.disabled = false;
        scanBtn.textContent = "scan";
      }
    },
  }, [
    el("div", { class: "stack", style: "padding: 1rem;" }, [
      el("label", {}, [
        el("span", {}, "repo path (init new project, or register an already-init'd one)"),
        repoInput,
      ]),
      el("div", { class: "row" }, [scanBtn]),
    ]),
  ]);
  return el("section", { class: "card" }, [
    el("header", {}, el("h2", {}, "register / init project")),
    scanForm,
    expanded,
  ]);
}

function emptyManifest() {
  return {
    project: { name: "", base_domain: "" },
    stack: { kind: "compose", file: "docker-compose.dev.yml", service: "" },
    expose: [],
  };
}

// renderManifestForm produces an editable form mirroring the manifest
// shape. Only the most-edited fields get dedicated inputs; the rest is
// passed through verbatim from the scan result so accepting defaults
// is one click.
function renderManifestForm(scanState, parentFlash) {
  const m = scanState.suggested;
  const projectName = el("input", {
    type: "text", name: "project_name", required: true,
    value: m.project?.name || "",
  });
  const baseDomain = el("input", {
    type: "text", name: "base_domain",
    value: m.project?.base_domain || "",
    placeholder: "(default: <name>.<tld>)",
  });
  const stackFile = el("input", {
    type: "text", name: "stack_file",
    value: m.stack?.file || "docker-compose.dev.yml",
  });
  const defaultService = el("input", {
    type: "text", name: "default_service",
    value: m.stack?.service || "",
    placeholder: "(no bare-slug alias)",
  });
  const matchHostUID = el("input", {
    type: "checkbox", name: "match_host_uid",
    value: "true",
  });
  if (m.stack?.match_host_uid) matchHostUID.checked = true;

  const exposeRows = el("div", { class: "stack", style: "gap: 0.4rem;" });
  function addExposeRow(rule = { service: "", port: 80, host: "" }) {
    const svc = el("input", { type: "text", name: "expose_service", value: rule.service, placeholder: "service", style: "flex: 1;" });
    const port = el("input", { type: "text", name: "expose_port", value: String(rule.port), placeholder: "port", style: "width: 6rem;" });
    const host = el("input", { type: "text", name: "expose_host", value: rule.host || "", placeholder: "host (default: service)", style: "flex: 1;" });
    const remove = el("button", { type: "button", class: "danger", onclick: (e) => { e.target.closest(".row").remove(); } }, "×");
    exposeRows.appendChild(el("div", { class: "row" }, [svc, port, host, remove]));
  }
  for (const r of m.expose || []) addExposeRow(r);
  if (!m.expose || m.expose.length === 0) {
    for (const s of scanState.services) addExposeRow({ service: s.name, port: s.port });
  }

  const flash = el("div");

  const form = el("form", {
    class: "stack",
    onsubmit: async (e) => {
      e.preventDefault();
      const rules = [];
      for (const row of exposeRows.children) {
        const inputs = row.querySelectorAll("input");
        const svc = inputs[0].value.trim();
        const port = parseInt(inputs[1].value, 10);
        const host = inputs[2].value.trim();
        if (!svc || !Number.isFinite(port)) continue;
        const rule = { service: svc, port };
        if (host) rule.host = host;
        rules.push(rule);
      }
      if (rules.length === 0) {
        flash.replaceChildren(el("div", { class: "flash error" }, "at least one expose rule is required"));
        return;
      }
      const manifest = {
        ...m,
        project: {
          name: projectName.value.trim(),
          base_domain: baseDomain.value.trim() || undefined,
        },
        stack: {
          ...(m.stack || { kind: "compose" }),
          file: stackFile.value.trim() || undefined,
          service: defaultService.value.trim() || undefined,
          match_host_uid: matchHostUID.checked || undefined,
        },
        expose: rules,
      };
      // Trim undefined fields server-side; JSON.stringify already drops them.
      flash.replaceChildren(el("div", { class: "flash" }, "writing manifest…"));
      try {
        const r = await api("/api/v1/projects", {
          method: "POST",
          body: { repo: scanState.repo, manifest },
        });
        const msg = r.warning
          ? `manifest written (${r.merged ? "merged" : "created"}), but registry: ${r.warning}`
          : `${r.merged ? "merged into" : "created"} ${r.manifest_path} · registered as ${r.project_name}`;
        if (parentFlash) parentFlash.replaceChildren(el("div", { class: r.warning ? "flash warn" : "flash success" }, msg));
        await loadProjects();
        location.hash = `#/projects/${encodeURIComponent(r.project_name)}`;
      } catch (err) {
        flash.replaceChildren(el("div", { class: "flash error" }, "create failed: " + err.message));
      }
    },
  }, [
    el("div", { class: "label", style: "padding: 0 1rem;" },
      scanState.isReinit ? `editing existing manifest at ${scanState.repo}` : `new project at ${scanState.repo}`),
    el("label", {}, [el("span", {}, "project name"), projectName]),
    el("label", {}, [el("span", {}, "base domain"), baseDomain]),
    el("label", {}, [el("span", {}, "compose file"), stackFile]),
    el("label", {}, [el("span", {}, "default service (gets bare-slug alias)"), defaultService]),
    el("label", { class: "row" }, [
      matchHostUID,
      el("span", {}, "match host uid (for distroless/nonroot images)"),
    ]),
    el("div", {}, [
      el("div", { class: "label" }, "expose rules"),
      exposeRows,
      el("button", { type: "button", onclick: () => addExposeRow(), style: "align-self: flex-start;" }, "+ add rule"),
    ]),
    el("div", { class: "row" }, [
      el("button", { class: "primary", type: "submit" }, scanState.isReinit ? "save manifest" : "create project"),
    ]),
    flash,
  ]);
  return form;
}

// ----- project detail (worktrees + create form) -----

async function renderProjectDetail(name) {
  view.appendChild(el("div", { class: "crumbs" }, [el("a", { href: "#/projects" }, "← projects")]));
  let p = store.projectDetail[name];
  if (!p) {
    try { await loadProjectDetail(name); p = store.projectDetail[name]; }
    catch (e) {
      if (e.status === 404) {
        view.appendChild(el("div", { class: "flash error" }, "project not found: " + name));
        return;
      }
      view.appendChild(el("div", { class: "flash error" }, "load failed: " + e.message));
      return;
    }
  }

  const flash = el("div");

  const headerCard = el("section", { class: "card" }, [
    el("header", {}, [
      el("div", {}, [
        el("h2", {}, p.name),
        el("div", { class: "branch", style: "margin-top: 0.25rem;" }, p.repo_path),
      ]),
      el("div", { class: "toolbar" }, [
        el("button", {
          class: "danger",
          onclick: () => unregisterProject(p.name, flash),
        }, "unregister"),
      ]),
    ]),
    el("table", { class: "kv" }, [
      el("tr", {}, [el("td", {}, "manifest"), el("td", {}, p.has_manifest ? "✓ present" : "— missing")]),
      el("tr", {}, [el("td", {}, "active workloads"), el("td", {}, String(p.active_workloads))]),
      el("tr", {}, [el("td", {}, "registered at"), el("td", {}, p.registered_at)]),
      p.manifest?.project?.base_domain
        ? el("tr", {}, [el("td", {}, "base domain"), el("td", {}, p.manifest.project.base_domain)])
        : null,
    ]),
  ]);
  view.append(flash, headerCard);

  // Worktrees + create form
  if (!store.projectWorktrees[name]) {
    try { await loadProjectWorktrees(name); }
    catch (e) {
      view.appendChild(el("div", { class: "flash error" }, "load worktrees failed: " + e.message));
      return;
    }
  }
  const worktrees = store.projectWorktrees[name] || [];

  view.appendChild(renderProjectWorktreesCard(p, worktrees, flash));
  view.appendChild(renderCreateWorktreeForm(p, flash));
}

function renderProjectWorktreesCard(p, worktrees, parentFlash) {
  const card = el("section", { class: "card" }, [
    el("header", {}, [
      el("h2", {}, "worktrees"),
      el("button", {
        onclick: async () => {
          await loadProjectWorktrees(p.name);
          render();
        },
      }, "refresh"),
    ]),
  ]);
  if (worktrees.length === 0) {
    card.appendChild(
      el("div", { class: "empty", style: "border: none; border-radius: 0;" },
        "no git worktrees yet — create one below"),
    );
    return card;
  }
  for (const wt of worktrees) {
    card.appendChild(renderWorktreeRow(p, wt, parentFlash));
  }
  return card;
}

function renderWorktreeRow(p, wt, parentFlash) {
  const detailHash = workloadDetailHash(p.name, wt.slug);
  const status = wt.has_workload ? wt.workload.status : "no-workload";
  const dotClass = wt.has_workload ? wt.workload.status : "";

  const flash = el("div", { style: "padding: 0 1rem 0.85rem;" });
  const actions = [];
  if (wt.has_workload) {
    actions.push(el("a", { class: "btn", href: detailHash }, "details"));
    if (wt.workload.status === "running") {
      actions.push(el("button", { class: "danger", onclick: () => doDown(p.name, wt.slug, flash) }, "down"));
    } else {
      actions.push(el("button", { class: "primary", onclick: () => doUp(p.name, wt.slug, wt.path, flash) }, "up"));
    }
  } else {
    actions.push(el("button", { class: "primary", onclick: () => doUp(p.name, wt.slug, wt.path, flash) }, "up"));
  }
  actions.push(el("button", {
    class: "danger",
    onclick: () => deleteWorktree(p, wt, parentFlash),
  }, "delete"));

  return el("div", {}, [
    el("div", { class: "workload" }, [
      el("div", { class: "ident" }, [
        el("span", { class: "slug" }, wt.has_workload
          ? el("a", { href: detailHash }, wt.slug)
          : wt.slug),
        el("span", { class: "branch" }, wt.branch || "(detached)"),
        el("span", { class: "status" }, [
          el("span", { class: `dot ${dotClass}` }),
          status,
        ]),
      ]),
      el("div", { class: "panels" }, [
        wt.has_workload ? urlList(wt.workload.urls) : null,
        wt.has_workload ? containerList(wt.workload.containers) : null,
        el("div", { class: "branch", style: "word-break: break-all;" }, wt.path),
      ]),
      el("div", { class: "actions" }, actions),
    ]),
    flash,
  ]);
}

function renderCreateWorktreeForm(p, parentFlash) {
  const flash = el("div");
  const form = el("form", {
    class: "stack",
    onsubmit: async (e) => {
      e.preventDefault();
      const data = Object.fromEntries(new FormData(e.target));
      const body = {
        project: p.name,
        slug: data.slug,
        branch: data.branch || undefined,
        from: data.from || undefined,
        up: !!data.up,
      };
      flash.replaceChildren(el("div", { class: "flash" }, "creating…"));
      try {
        const r = await api("/api/v1/worktrees", { method: "POST", body });
        flash.replaceChildren(el("div", { class: "flash success" },
          `created at ${r.worktree_path} (branch ${r.branch})${r.workload ? " · workload up" : ""}`));
        e.target.reset();
        await loadProjectWorktrees(p.name);
        if (r.workload) {
          const i = store.workloads.findIndex(
            (w) => w.project === p.name && w.slug === r.workload.slug,
          );
          if (i >= 0) store.workloads[i] = r.workload;
          else store.workloads.push(r.workload);
        }
        render();
      } catch (err) {
        flash.replaceChildren(el("div", { class: "flash error" }, "create failed: " + err.message));
      }
    },
  }, [
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
    ]),
    flash,
  ]);
  return el("section", { class: "card" }, [
    el("header", {}, el("h2", {}, "new worktree")),
    form,
  ]);
}

async function unregisterProject(name, flash) {
  if (!confirm(`Unregister "${name}"?\n\nThis only removes the registry entry. The .pier.toml and any worktrees stay on disk.`)) return;
  flash.replaceChildren(el("div", { class: "flash" }, "unregistering…"));
  try {
    await api(`/api/v1/projects/${encodeURIComponent(name)}`, { method: "DELETE" });
    delete store.projectDetail[name];
    delete store.projectWorktrees[name];
    store.projects = null;
    location.hash = "#/projects";
  } catch (e) {
    flash.replaceChildren(el("div", { class: "flash error" }, "unregister failed: " + e.message));
  }
}

async function deleteWorktree(p, wt, flash) {
  if (!confirm(`Delete worktree "${wt.slug}" at ${wt.path}?\n\npier will run "down" first if a workload is up.`)) return;
  flash.replaceChildren(el("div", { class: "flash" }, "deleting…"));
  try {
    const r = await api(
      `/api/v1/worktrees/${encodeURIComponent(wt.slug)}?project=${encodeURIComponent(p.name)}`,
      { method: "DELETE" },
    );
    flash.replaceChildren(
      el("div", { class: r.warning ? "flash warn" : "flash success" },
        r.warning || `removed ${wt.slug}`),
    );
    store.workloads = store.workloads.filter(
      (x) => !(x.project === p.name && x.slug === wt.slug),
    );
    await loadProjectWorktrees(p.name);
    render();
  } catch (e) {
    flash.replaceChildren(el("div", { class: "flash error" }, "delete failed: " + e.message));
  }
}

// ----- workload detail (project-scoped) -----

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
    el("a", { href: `#/projects/${encodeURIComponent(project)}` }, `← ${project}`),
  ]);
  const running = w.status === "running";
  const logsHash = `#/projects/${encodeURIComponent(project)}/workload/${encodeURIComponent(slug)}/logs`;

  const flash = el("div");
  const card = el("section", { class: "card" }, [
    el("header", {}, [
      el("h2", {}, `${project} / ${slug}`),
      el("div", { class: "toolbar" }, [
        el("a", { class: "btn", href: logsHash }, "logs"),
        running
          ? el("button", { class: "danger", onclick: () => doDown(project, slug, flash) }, "down")
          : el("button", { class: "primary", onclick: () => doUp(project, slug, w.worktree_path, flash) }, "up"),
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

async function doUp(project, slug, worktreePath, flash) {
  flash.replaceChildren(el("div", { class: "flash" }, "starting…"));
  try {
    const wl = await api(
      `/api/v1/workloads/${encodeURIComponent(project)}/${encodeURIComponent(slug)}/up`,
      {
        method: "POST",
        // worktree_path needed only when no state row exists yet
        // (first up on a freshly-created worktree).
        body: worktreePath ? { worktree_path: worktreePath } : undefined,
      },
    );
    flash.replaceChildren(el("div", { class: "flash success" }, `up — status ${wl.status}`));
    const i = store.workloads.findIndex((w) => w.project === project && w.slug === slug);
    if (i >= 0) store.workloads[i] = wl;
    else store.workloads.push(wl);
    if (store.projectWorktrees[project]) await loadProjectWorktrees(project);
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
    if (store.projectWorktrees[project]) await loadProjectWorktrees(project);
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
    el("a", { href: workloadDetailHash(project, slug) }, `← ${project} / ${slug}`),
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
