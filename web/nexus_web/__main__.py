"""Entry point: `python -m nexus_web --socket <path> --port <port>`.

Started as a normal nexus service (see web/nexus.yaml). Connects to the daemon's
Unix socket and serves the public HTTP UI.
"""

from __future__ import annotations

import argparse
import os

import uvicorn

from .app import create_app
from .client import NexusClient


def main(argv: list[str] | None = None) -> None:
    parser = argparse.ArgumentParser(prog="nexus_web")
    parser.add_argument(
        "--socket",
        default=os.environ.get("NEXUS_SOCKET", ""),
        help="path to the daemon Unix socket (default: $NEXUS_HOME/nexus.sock)",
    )
    parser.add_argument("--host", default="0.0.0.0", help="bind host (default 0.0.0.0)")
    parser.add_argument("--port", type=int, default=7777, help="bind port (default 7777)")
    args = parser.parse_args(argv)

    socket_path = args.socket
    if not socket_path:
        home = os.environ.get("NEXUS_HOME", "")
        if not home:
            parser.error("no --socket given and NEXUS_HOME is unset")
        socket_path = os.path.join(home, "nexus.sock")

    client = NexusClient(socket_path)
    app = create_app(client)
    uvicorn.run(app, host=args.host, port=args.port, log_level="info")


if __name__ == "__main__":
    main()
