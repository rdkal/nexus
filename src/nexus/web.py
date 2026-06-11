import os
import secrets
from fastapi import Depends, FastAPI, HTTPException, status
from fastapi.responses import HTMLResponse
from fastapi.security import HTTPBasic, HTTPBasicCredentials
import uvicorn

app = FastAPI(title="Nexus")

PREFECT_UI_URL = os.environ.get("PREFECT_UI_URL", "http://localhost:4200")
NEXUS_USER = os.environ.get("NEXUS_USER", "")
NEXUS_PASSWORD = os.environ.get("NEXUS_PASSWORD", "")

_security = HTTPBasic(auto_error=False)


def _require_auth(credentials: HTTPBasicCredentials | None = Depends(_security)) -> None:
    if not NEXUS_USER:
        return  # auth disabled — neither var is set
    if credentials is None or not (
        secrets.compare_digest(credentials.username.encode(), NEXUS_USER.encode())
        and secrets.compare_digest(credentials.password.encode(), NEXUS_PASSWORD.encode())
    ):
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            headers={"WWW-Authenticate": 'Basic realm="Nexus"'},
        )


_HTML = f"""<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Nexus</title>
  <style>
    * {{ box-sizing: border-box; margin: 0; padding: 0; }}
    body {{ font-family: system-ui, sans-serif; background: #f5f5f5; color: #222; }}
    header {{ background: #1a1a2e; color: #fff; padding: 24px 40px; }}
    header h1 {{ font-size: 1.6rem; font-weight: 600; letter-spacing: 0.05em; }}
    main {{ max-width: 800px; margin: 48px auto; padding: 0 24px; }}
    .card {{
      background: #fff;
      border: 1px solid #e0e0e0;
      border-radius: 10px;
      padding: 28px 32px;
      margin-bottom: 20px;
      display: flex;
      align-items: center;
      justify-content: space-between;
    }}
    .card h2 {{ font-size: 1.1rem; margin-bottom: 4px; }}
    .card p {{ color: #666; font-size: 0.9rem; }}
    .btn {{
      display: inline-block;
      background: #1a1a2e;
      color: #fff;
      padding: 10px 20px;
      border-radius: 6px;
      text-decoration: none;
      font-size: 0.9rem;
      white-space: nowrap;
    }}
    .btn:hover {{ background: #16213e; }}
  </style>
</head>
<body>
  <header><h1>Nexus</h1></header>
  <main>
    <div class="card">
      <div>
        <h2>Prefect UI</h2>
        <p>Workflow runs, deployments, and schedules</p>
      </div>
      <a class="btn" href="{PREFECT_UI_URL}" target="_blank">Open →</a>
    </div>
  </main>
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
