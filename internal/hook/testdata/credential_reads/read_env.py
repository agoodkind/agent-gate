from __future__ import annotations

from pathlib import Path

SOURCE_PATH = "{{ROOT}}/.env"

print(Path(SOURCE_PATH).read_text())
