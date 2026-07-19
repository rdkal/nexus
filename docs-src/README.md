# nexus docs site

Source for the nexus documentation site at
[rdkal.github.io/nexus](https://rdkal.github.io/nexus/). A single static page
built with [iris](https://rdkal.github.io/iris) and served by GitHub Pages from
the repo's /docs folder.

## Rebuild

```sh
python3 -m venv .venv
.venv/bin/pip install -r requirements.txt
.venv/bin/python build.py          # writes ../docs/index.html
```

`build.py` renders one self-contained `index.html` (inline CSS, no external
assets) plus a `.nojekyll` marker. Commit the regenerated `docs/` to publish.

## GitHub Pages setup (one time)

Settings → Pages → Build and deployment → **Deploy from a branch** →
branch `main`, folder `/docs`.
