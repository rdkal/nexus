"""Nexus web portal — FastAPI on port 8080."""
import asyncio
import json
import os
import secrets
import subprocess
from pathlib import Path

import httpx
from fastapi import Depends, FastAPI, HTTPException, status
from fastapi.responses import HTMLResponse, JSONResponse, StreamingResponse
from fastapi.security import HTTPBasic, HTTPBasicCredentials
import uvicorn

from nexus.config import load_config

app = FastAPI(title="Nexus")

PREFECT_UI_URL = os.environ.get("PREFECT_UI_URL", "http://localhost:4200")
NEXUS_USER = os.environ.get("NEXUS_USER", "")
NEXUS_PASSWORD = os.environ.get("NEXUS_PASSWORD", "")
NEXUS_HOME = Path(os.environ.get("NEXUS_HOME", Path.home() / ".nexus"))
PC_PORT = int(os.environ.get("PROCESS_COMPOSE_PORT", 9080))
PC_BASE = f"http://localhost:{PC_PORT}"

NEXUS_SERVICES = ["prefect-server", "prefect-worker", "nexus-poller", "nexus-web"]

_security = HTTPBasic(auto_error=False)


def _require_auth(credentials: HTTPBasicCredentials | None = Depends(_security)) -> None:
    if not NEXUS_USER:
        return
    if credentials is None or not (
        secrets.compare_digest(credentials.username.encode(), NEXUS_USER.encode())
        and secrets.compare_digest(credentials.password.encode(), NEXUS_PASSWORD.encode())
    ):
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            headers={"WWW-Authenticate": 'Basic realm="Nexus"'},
        )


# ── meta ──────────────────────────────────────────────────────────────────────

@app.get("/api/meta", dependencies=[Depends(_require_auth)])
def api_meta():
    return {"prefect_ui_url": PREFECT_UI_URL}


# ── process-compose proxy ─────────────────────────────────────────────────────

@app.get("/api/processes", dependencies=[Depends(_require_auth)])
async def api_processes():
    try:
        async with httpx.AsyncClient() as client:
            r = await client.get(f"{PC_BASE}/processes", timeout=5)
            return r.json()
    except Exception as e:
        return JSONResponse({"error": str(e), "data": []}, status_code=503)


@app.post("/api/process/start/{name}", dependencies=[Depends(_require_auth)])
async def api_start(name: str):
    try:
        async with httpx.AsyncClient() as client:
            r = await client.post(f"{PC_BASE}/process/start/{name}", timeout=10)
            return r.json() if r.content else {}
    except Exception as e:
        return JSONResponse({"error": str(e)}, status_code=503)


@app.patch("/api/process/stop/{name}", dependencies=[Depends(_require_auth)])
async def api_stop(name: str):
    try:
        async with httpx.AsyncClient() as client:
            r = await client.patch(f"{PC_BASE}/process/stop/{name}", timeout=10)
            return r.json() if r.content else {}
    except Exception as e:
        return JSONResponse({"error": str(e)}, status_code=503)


@app.post("/api/process/restart/{name}", dependencies=[Depends(_require_auth)])
async def api_restart(name: str):
    try:
        async with httpx.AsyncClient() as client:
            r = await client.post(f"{PC_BASE}/process/restart/{name}", timeout=10)
            return r.json() if r.content else {}
    except Exception as e:
        return JSONResponse({"error": str(e)}, status_code=503)


@app.get("/api/process/logs/{name}", dependencies=[Depends(_require_auth)])
async def api_logs(name: str, limit: int = 200):
    try:
        async with httpx.AsyncClient() as client:
            r = await client.get(f"{PC_BASE}/process/logs/{name}/0/{limit}", timeout=5)
            return r.json()
    except Exception as e:
        return JSONResponse({"error": str(e), "logs": []}, status_code=503)


@app.get("/api/process/logs/{name}/stream", dependencies=[Depends(_require_auth)])
async def api_logs_stream(name: str):
    """SSE endpoint; polls PC HTTP logs API and forwards new lines as events."""
    async def event_stream():
        prev: list = []
        while True:
            try:
                async with httpx.AsyncClient() as client:
                    r = await client.get(
                        f"{PC_BASE}/process/logs/{name}/0/500",
                        timeout=5,
                    )
                    logs: list = r.json().get("logs") or []
                    if len(logs) > len(prev):
                        for line in logs[len(prev):]:
                            yield f"data: {json.dumps(line)}\n\n"
                    elif logs != prev and logs:
                        # Process restarted — clear client view and resend
                        yield "event: clear\ndata: {}\n\n"
                        for line in logs:
                            yield f"data: {json.dumps(line)}\n\n"
                    prev = logs
            except Exception:
                pass
            await asyncio.sleep(0.5)

    return StreamingResponse(
        event_stream(),
        media_type="text/event-stream",
        headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"},
    )


# ── config + apps ─────────────────────────────────────────────────────────────

@app.get("/api/config", dependencies=[Depends(_require_auth)])
def api_config():
    try:
        config = load_config(NEXUS_HOME / "config.yaml")
    except Exception as e:
        return JSONResponse({"error": str(e)}, status_code=500)
    return {
        "project": config.project,
        "env_keys": list(config.env.keys()),
        "includes": [
            {
                "name": inc.name,
                "repo": inc.repo,
                "ref": inc.ref,
                "poll_interval": inc.poll_interval,
                "env_keys": list(inc.env.keys()),
            }
            for inc in config.includes
        ],
    }


@app.get("/api/apps", dependencies=[Depends(_require_auth)])
def api_apps():
    try:
        config = load_config(NEXUS_HOME / "config.yaml")
    except Exception:
        return []
    result = []
    for inc in config.includes:
        app_dir = NEXUS_HOME / "apps" / inc.name
        exists = app_dir.exists()
        sha = ""
        if exists:
            proc = subprocess.run(
                ["git", "-C", str(app_dir), "rev-parse", "--short", "HEAD"],
                capture_output=True, text=True,
            )
            sha = proc.stdout.strip()
        result.append({
            "name": inc.name,
            "repo": inc.repo,
            "ref": inc.ref,
            "sha": sha,
            "exists": exists,
        })
    return result


# ── HTML portal ───────────────────────────────────────────────────────────────

_HTML = """<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Nexus</title>
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body { font-family: system-ui, sans-serif; background: #f5f5f5; color: #222; }
    header { background: #1a1a2e; color: #fff; padding: 20px 40px; display: flex; align-items: center; justify-content: space-between; }
    header h1 { font-size: 1.4rem; font-weight: 600; letter-spacing: 0.05em; }
    main { max-width: 900px; margin: 32px auto; padding: 0 24px; }
    h2 { font-size: 0.72rem; font-weight: 700; letter-spacing: 0.12em; text-transform: uppercase; color: #888; margin-bottom: 10px; }
    section { margin-bottom: 28px; }
    .card { background: #fff; border: 1px solid #e0e0e0; border-radius: 10px; overflow: hidden; }
    .row { display: flex; align-items: center; padding: 13px 20px; gap: 12px; border-bottom: 1px solid #f0f0f0; }
    .row:last-child { border-bottom: none; }
    .dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; background: #ccc; }
    .dot.running { background: #22c55e; }
    .dot.stopped, .dot.completed { background: #ef4444; }
    .dot.starting, .dot.launching { background: #f59e0b; }
    .row-name { font-weight: 500; font-size: 0.92rem; min-width: 150px; }
    .row-meta { color: #555; font-size: 0.84rem; flex: 1; }
    .row-status { font-size: 0.78rem; color: #888; min-width: 70px; }
    .actions { display: flex; gap: 5px; }
    .btn { display: inline-flex; align-items: center; gap: 4px; padding: 5px 10px; border-radius: 6px; font-size: 0.78rem; cursor: pointer; border: 1px solid #e0e0e0; background: #fff; color: #444; text-decoration: none; white-space: nowrap; font-family: inherit; }
    .btn:hover { background: #f5f5f5; }
    .btn.primary { background: #1a1a2e; color: #fff; border-color: #1a1a2e; }
    .btn.primary:hover { background: #16213e; }
    .btn.danger { color: #dc2626; border-color: #fca5a5; }
    .btn.danger:hover { background: #fef2f2; }
    .tag { display: inline-block; background: #f3f4f6; border-radius: 4px; padding: 1px 7px; font-family: monospace; font-size: 0.78rem; color: #555; }
    .badge { font-size: 0.73rem; padding: 2px 8px; border-radius: 4px; font-weight: 500; }
    .badge.ok { background: #dcfce7; color: #166534; }
    .badge.err { background: #fee2e2; color: #991b1b; }
    .mono { font-family: monospace; font-size: 0.84rem; }
    .error-banner { background: #fee2e2; border: 1px solid #fca5a5; border-radius: 8px; padding: 12px 20px; color: #991b1b; margin-bottom: 20px; font-size: 0.88rem; }
    .log-toolbar { display: flex; gap: 6px; padding: 7px 12px; border-top: 1px solid #e8e8e8; background: #fafafa; }
    .log-panel { display: none; background: #111827; color: #d1d5db; font-family: monospace; font-size: 0.78rem; padding: 10px 14px; max-height: 280px; overflow-y: auto; border-top: 1px solid #e0e0e0; }
    .log-panel.open { display: block; }
    .log-line { white-space: pre-wrap; line-height: 1.6; }
    .muted { color: #9ca3af; font-style: italic; }
  </style>
</head>
<body>
<header>
  <h1>Nexus</h1>
  <button class="btn" style="color:#fff;border-color:rgba(255,255,255,.3);background:transparent" onclick="loadAll()">&#8635; Refresh</button>
</header>
<main>
  <div id="error-banner" class="error-banner" style="display:none"></div>

  <section>
    <h2>Links</h2>
    <div class="card">
      <div class="row">
        <div class="row-name">Prefect UI</div>
        <div class="row-meta">Workflow runs, deployments, and schedules</div>
        <a id="prefect-link" class="btn primary" href="#" target="_blank">Open &#8594;</a>
      </div>
    </div>
  </section>

  <section>
    <h2>Services</h2>
    <div class="card" id="services-card">
      <div class="row"><span class="row-meta muted">Loading&#8230;</span></div>
    </div>
  </section>

  <section>
    <h2>Apps</h2>
    <div class="card" id="apps-card">
      <div class="row"><span class="row-meta muted">Loading&#8230;</span></div>
    </div>
  </section>

  <section>
    <h2>Config</h2>
    <div class="card" id="config-card">
      <div class="row"><span class="row-meta muted">Loading&#8230;</span></div>
    </div>
  </section>
</main>

<script>
const NEXUS_SERVICES = ["prefect-server","prefect-worker","nexus-poller","nexus-web"];
const eventSources = {};

async function loadAll() {
  const [metaR, procsR, appsR, cfgR] = await Promise.allSettled([
    fetch('/api/meta').then(r => r.json()),
    fetch('/api/processes').then(r => r.json()),
    fetch('/api/apps').then(r => r.json()),
    fetch('/api/config').then(r => r.json()),
  ]);
  if (metaR.status === 'fulfilled') {
    document.getElementById('prefect-link').href = metaR.value.prefect_ui_url;
  }
  renderServices(procsR.status === 'fulfilled' ? procsR.value : null);
  renderApps(appsR.status === 'fulfilled' ? appsR.value : null);
  renderConfig(cfgR.status === 'fulfilled' ? cfgR.value : null);
}

function dotClass(st) {
  if (!st) return '';
  const s = st.toLowerCase();
  if (s === 'running') return 'running';
  if (s === 'completed' || s === 'stopped') return 'stopped';
  return 'starting';
}

function renderServices(data) {
  const card = document.getElementById('services-card');
  if (!data || data.error) {
    card.innerHTML = '<div class="row"><span class="row-meta muted">' + escHtml((data && data.error) ? data.error : 'Could not reach process-compose API (port 9080)') + '</span></div>';
    return;
  }
  const byName = {};
  (data.data || []).forEach(p => { byName[p.name] = p; });
  card.innerHTML = NEXUS_SERVICES.map(name => {
    const p = byName[name];
    const st = p ? p.status : 'unknown';
    const self = name === 'nexus-web';
    const ctrl = self ? '' : [
      '<div class="actions">',
      '<button class="btn danger" onclick="svcAction(\'stop\',\'' + name + '\')">&#9632; Stop</button>',
      '<button class="btn" onclick="svcAction(\'start\',\'' + name + '\')">&#9654; Start</button>',
      '<button class="btn" onclick="svcAction(\'restart\',\'' + name + '\')">&#8635; Restart</button>',
      '<button class="btn" onclick="toggleLogs(\'' + name + '\')">Logs</button>',
      '</div>',
    ].join('');
    const logArea = self ? '' : [
      '<div class="log-toolbar" id="log-tb-' + name + '" style="display:none">',
      '<button class="btn" onclick="loadLogs(\'' + name + '\')">&#8635; Reload</button>',
      '<button class="btn" id="stream-btn-' + name + '" onclick="toggleStream(\'' + name + '\')">&#9654; Stream</button>',
      '</div>',
      '<div class="log-panel" id="log-' + name + '"></div>',
    ].join('');
    return '<div><div class="row"><span class="dot ' + dotClass(st) + '"></span><span class="row-name">' + escHtml(name) + '</span><span class="row-status">' + escHtml(st) + '</span><span style="flex:1"></span>' + ctrl + '</div>' + logArea + '</div>';
  }).join('');
}

function renderApps(data) {
  const card = document.getElementById('apps-card');
  if (!Array.isArray(data) || data.length === 0) {
    card.innerHTML = '<div class="row"><span class="row-meta muted">No apps configured</span></div>';
    return;
  }
  card.innerHTML = data.map(a =>
    '<div class="row"><span class="row-name">' + escHtml(a.name) + '</span>' +
    '<span class="row-meta mono">' + escHtml(a.repo) + (a.ref !== 'main' ? '@' + escHtml(a.ref) : '') + '</span>' +
    '<span class="tag">' + escHtml(a.sha || '—') + '</span>' +
    '<span class="badge ' + (a.exists ? 'ok' : 'err') + '">' + (a.exists ? 'cloned' : 'missing') + '</span>' +
    '</div>'
  ).join('');
}

function renderConfig(data) {
  const banner = document.getElementById('error-banner');
  const card = document.getElementById('config-card');
  if (!data || data.error) {
    banner.textContent = (data && data.error) ? data.error : 'Failed to load config.yaml';
    banner.style.display = '';
    card.innerHTML = '<div class="row"><span class="row-meta muted">Config unavailable</span></div>';
    return;
  }
  banner.style.display = 'none';
  let html = '';
  if (data.project) {
    html += '<div class="row"><span class="row-name">Project</span><span class="row-meta">' + escHtml(data.project) + '</span></div>';
  }
  if (data.env_keys && data.env_keys.length) {
    html += '<div class="row"><span class="row-name">Env keys</span><span class="row-meta">' + data.env_keys.map(k => '<span class="tag">' + escHtml(k) + '</span>').join(' ') + '</span></div>';
  }
  (data.includes || []).forEach(inc => {
    const envPart = inc.env_keys.length ? ' <span style="color:#888;font-size:.8rem">env: ' + inc.env_keys.map(escHtml).join(', ') + '</span>' : '';
    html += '<div class="row"><span class="row-name">' + escHtml(inc.name) + '</span><span class="row-meta mono">' + escHtml(inc.repo) + '@' + escHtml(inc.ref) + '</span><span class="row-status">' + inc.poll_interval + 's</span>' + envPart + '</div>';
  });
  if (!html) {
    html = '<div class="row"><span class="row-meta muted">Empty config</span></div>';
  }
  card.innerHTML = html;
}

async function svcAction(action, name) {
  const method = action === 'stop' ? 'PATCH' : 'POST';
  await fetch('/api/process/' + action + '/' + name, {method: method});
  setTimeout(loadAll, action === 'restart' ? 2000 : 1000);
}

function toggleLogs(name) {
  const panel = document.getElementById('log-' + name);
  const tb = document.getElementById('log-tb-' + name);
  if (panel.classList.contains('open')) {
    panel.classList.remove('open');
    tb.style.display = 'none';
    stopStream(name);
  } else {
    panel.classList.add('open');
    tb.style.display = '';
    loadLogs(name);
  }
}

async function loadLogs(name) {
  const panel = document.getElementById('log-' + name);
  panel.innerHTML = '<div class="log-line muted">Loading&#8230;</div>';
  try {
    const data = await fetch('/api/process/logs/' + name).then(r => r.json());
    const logs = data.logs || [];
    panel.innerHTML = logs.length
      ? logs.map(l => '<div class="log-line">' + escHtml(typeof l === 'string' ? l : JSON.stringify(l)) + '</div>').join('')
      : '<div class="log-line muted">(no logs)</div>';
    panel.scrollTop = panel.scrollHeight;
  } catch(e) {
    panel.innerHTML = '<div class="log-line" style="color:#f87171">Error: ' + escHtml(String(e)) + '</div>';
  }
}

function toggleStream(name) {
  if (eventSources[name]) { stopStream(name); } else { startStream(name); }
}

function startStream(name) {
  const panel = document.getElementById('log-' + name);
  const btn = document.getElementById('stream-btn-' + name);
  if (btn) btn.textContent = '■ Stop stream';
  const es = new EventSource('/api/process/logs/' + name + '/stream');
  eventSources[name] = es;
  es.addEventListener('clear', function() { panel.innerHTML = ''; });
  es.onmessage = function(e) {
    const line = JSON.parse(e.data);
    const div = document.createElement('div');
    div.className = 'log-line';
    div.textContent = typeof line === 'string' ? line : JSON.stringify(line);
    panel.appendChild(div);
    panel.scrollTop = panel.scrollHeight;
  };
  es.onerror = function() { stopStream(name); };
}

function stopStream(name) {
  if (eventSources[name]) { eventSources[name].close(); delete eventSources[name]; }
  const btn = document.getElementById('stream-btn-' + name);
  if (btn) btn.textContent = '► Stream';
}

function escHtml(s) {
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}

loadAll();
setInterval(loadAll, 30000);
</script>
</body>
</html>"""


@app.get("/", response_class=HTMLResponse, dependencies=[Depends(_require_auth)])
def index():
    return _HTML


def main():
    port = int(os.environ.get("NEXUS_PORT", 8080))
    uvicorn.run(app, host="0.0.0.0", port=port, log_level="warning")


if __name__ == "__main__":
    main()
