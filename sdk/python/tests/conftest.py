"""Make `import httpx`/`import httpcore` (used by respx) resolve to httpx2/httpcore2."""

import httpx2

httpx2.alias_httpx()
