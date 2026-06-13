# Nexus — Design Document

## Overview

Nexus is a git-native process manager and deployment system. It turns a collection
of git repositories into a self-managing, continuously-deployed system running
directly on the host — no containers, no cloud primitives. The only delivery
mechanism is git; the only configuration format is YAML.

A single `curl` invocation installs the daemon. From that point, everything —
including updates to nexus itself — is driven by commits.

---

## Core Concepts

| Concept | Description |
|---|---|
| **Repo** | A git repository with a `nexus.yaml` at its root |
| **Deployment** | The unit nexus manages: a build command, named services, and volumes, all versioned together |
| **Service** | A named, long-running process within a deployment. Nexus starts it and supervises it |
| **Worktree** | An isolated checkout of a repo at a specific commit SHA. One active worktree per deployment |
| **Volume** | A directory that lives outside worktrees and persists across deployments |

---

## Installation

```sh
curl https://github.com/rdkal/nexus/raw/main/install.sh | sh -s -- \
  --source https://github.com/myorg/my-system
```

The install script:

1. Installs the `nexus` binary to `/usr/local/bin/nexus`
2. Creates `/var/nexus/` for runtime state
3. Registers and starts a system service (`systemd` on Linux, `launchd` on macOS)
4. Clones the given `--source` repo as the **root repo**

The root repo's `nexus.yaml` is the entry point for the entire system. Because nexus
manages itself the same way it manages any other repo, updating nexus is just a commit.

---

## nexus.yaml Specification

Every repo that nexus manages has a `nexus.yaml` at its root. The format is the same
whether it is the root repo or a repo pulled in transitively.

```yaml
# nexus.yaml

name: api-service

# Optional: pull in more repos. Each must have its own nexus.yaml.
# This is how you compose a whole system from independent repos.
repos:
  - url: https://github.com/myorg/worker-service
    ref: main
  - url: https://github.com/myorg/shared-infra
    ref: v2.1.0

# The deployment defined by this repo.
# All fields below are optional — a nexus.yaml that only lists repos is valid.

# build runs once inside the new worktree before services are started.
# A non-zero exit aborts the deployment; current services keep running.
build: pip install -e . && alembic upgrade head

# Persistent directories that survive across deployments.
# Accessible inside build and service commands as $NEXUS_VOLUME_<NAME> (uppercased).
volumes:
  - name: data
    path: /var/nexus/volumes/api-service-data
  - name: config
    path: /var/nexus/volumes/api-service-config

# Named long-running processes. Nexus starts them, supervises them, and stops them.
services:
  - name: api
    run: uvicorn app:main --host 0.0.0.0 --port 8080
  - name: worker
    run: celery -A app.tasks worker --concurrency 4
```

### Field Reference

#### Top-level

| Field | Type | Description |
|---|---|---|
| `name` | string | Human label, used in the UI and logs |
| `repos` | list | Other repos to pull in and manage. Each needs its own `nexus.yaml` |
| `build` | string | Shell command run in the new worktree before startup. Optional |
| `volumes` | list | Persistent directories. Created on first use if absent |
| `services` | list | Long-running processes nexus supervises |

#### `repos[]`

| Field | Required | Description |
|---|---|---|
| `url` | yes | Git-cloneable URL |
| `ref` | no | Branch, tag, or SHA. Defaults to `main` |

#### `volumes[]`

| Field | Required | Description |
|---|---|---|
| `name` | yes | Identifier. Exposed in the environment as `$NEXUS_VOLUME_<NAME>` |
| `path` | yes | Absolute path on host. Created if it does not exist |

#### `services[]`

| Field | Required | Description |
|---|---|---|
| `name` | yes | Unique within the deployment. Used in logs and the UI |
| `run` | yes | Shell command. Runs with `sh -c` from the worktree root |

---

## Environment Available to Build and Services

Nexus injects these variables into every `build` command and every `service` process:

```sh
NEXUS_REPO=<name>
NEXUS_SHA=<full-commit-sha>
NEXUS_REF=<branch-or-tag>
NEXUS_WORKTREE=<absolute-path-to-this-worktree>
NEXUS_VOLUME_<NAME>=<path>     # one per declared volume
```

---

## Deployment Lifecycle

This is the exact sequence nexus follows when a new commit is detected.

```
1. DETECT
   ├── Poll `git fetch origin <ref>` on an interval (default: 30s)
   └── New SHA differs from last-known → proceed

2. CHECKOUT
   └── git worktree add /var/nexus/repos/<slug>/worktrees/<new-sha> <new-sha>

3. BUILD  (runs inside the new worktree)
   ├── Execute the `build` command with sh -c
   ├── Capture stdout/stderr to a log file
   ├── Exit 0 → proceed to SWAP
   └── Non-zero exit → remove the failed worktree, keep current deployment running

4. SWAP
   ├── 4a. SHUTDOWN current services
   │       SIGTERM to every supervised service process
   │       Wait up to 30s for graceful exit
   │       SIGKILL anything still running
   │
   └── 4b. STARTUP new services
           For each service in the new deployment:
             spawn `run` command from the new worktree
             register PID with nexus supervisor
           On any service crashing immediately → ROLLBACK (see below)

5. PROMOTE
   ├── Record new SHA as active in nexus.db
   └── Old worktree is now eligible for removal

6. CLEANUP
   └── git worktree remove <old-sha>  (best-effort, retried on next cycle)
```

### Rollback

If one or more services crash within 5 seconds of startup:

1. Kill all services from the new deployment
2. Re-start each service from the **previous** worktree (which is still present)
3. Mark the new SHA as `failed` in nexus.db
4. Keep the failed worktree on disk until next successful deployment (for log inspection)

---

## Process Supervision

Nexus is the process supervisor for all services. It does not rely on systemd units
or any external supervisor per service.

- Each service process is spawned as a direct child of the nexus daemon
- On unexpected exit, nexus restarts with exponential backoff: 1s, 2s, 4s … cap 60s
- A service that crashes more than 5 times within 60 seconds is marked `degraded`
  and is not restarted automatically; the UI shows an alert
- On a planned SHUTDOWN (new deployment arriving), nexus sends SIGTERM, then after
  a grace period (default 30s, not yet configurable in v1) sends SIGKILL

Services bind their own ports. Nexus does not proxy or manage port allocation;
it only tracks PID, uptime, and restart count.

---

## Directory Layout (runtime)

```
/var/nexus/
  repos/
    <repo-slug>/
      .git/                    ← bare clone
      worktrees/
        <sha>/                 ← one checkout per deployment attempt
  volumes/
    <volume-name>/             ← persistent, survives deployments
  nexus.db                     ← sqlite: deployments, service state, logs
  logs/
    <repo-slug>/
      <sha>-build.log
      <sha>-<service-name>.log
```

---

## Composable / Recursive Systems

The `repos` field makes nexus recursive. The root repo defines the system boundary;
each referenced repo is pulled in and managed independently. Those repos can
themselves list more repos.

```
root nexus.yaml
  └── repos:
        ├── api-service/nexus.yaml
        │     └── (build + services, no child repos)
        ├── worker/nexus.yaml
        │     └── repos:
        │           └── shared-lib/nexus.yaml
        └── infra/nexus.yaml
              └── (volumes only, no services)
```

Each node watches its own `ref`, manages its own worktrees, and deploys
independently. There is no cross-repo ordering or dependency in v1.

---

## Web UI

Nexus serves a minimal HTTP UI on a configurable port (default `7777`).
No TLS, no authentication — intended for private network use.

### Pages

| Page | Content |
|---|---|
| `/` | All repos, their current SHA, service health at a glance |
| `/repos/<slug>` | Deployment history, build log per SHA, service status |
| `/repos/<slug>/logs/<sha>/<service>` | Live-tailed service log |
| `/volumes` | Declared volumes and disk usage |

### API

```
GET  /api/repos
GET  /api/repos/<slug>
GET  /api/repos/<slug>/deployments
POST /api/repos/<slug>/redeploy     ← re-run current SHA (e.g. after config change)
GET  /api/services
POST /api/services/<id>/restart
```

---

## Security Posture (v1)

- HTTP only, no authentication
- Intended for private networks / behind a firewall
- All processes run as the nexus daemon user (no privilege escalation)

---

## What Is Explicitly Out of Scope for v1

- **Flows / pipelines** — deferred. The deployment lifecycle (build + signal + start)
  covers the immediate need. Flows are the planned v2 addition.
- TLS / authentication on the web UI
- Inbound webhooks (git push events) — polling only for now
- Secret management — secrets must be provided via volumes or host environment
- Cross-repo dependency ordering
- Multi-machine / distributed execution
- Windows support

---

## Open Questions

1. **Concurrent commits**: if two commits arrive during one build, does nexus queue
   them or let the latest SHA supersede the in-flight build?
   Proposal: drop in-flight build, start fresh with the newest SHA.

2. **Service port declarations**: should `services` have an optional `ports` field
   so the UI can surface clickable URLs?

3. **Graceful shutdown timeout**: should this be configurable per service in v1, or
   is a single daemon-level default (30s) enough to start?

4. **Nexus self-update**: the root repo can define services that are nexus itself.
   The daemon receiving SIGTERM mid-swap needs care. This warrants a dedicated
   self-upgrade path.
