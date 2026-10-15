#!/usr/bin/env python3
"""Check tracked artifacts and preserved upstream license digests before release."""
import hashlib
import json
from pathlib import Path
import re
import subprocess

root = Path(__file__).resolve().parents[1]
paths = subprocess.check_output(["git", "ls-files", "-z"], cwd=root).decode().split("\0")
patterns = {
    "private key": rb"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----",
    "GitHub token": rb"\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{32,}\b",
    "model API token": rb"\bsk-[A-Za-z0-9_-]{32,}\b",
}
errors = []
for name in filter(None, paths):
    p = Path(name)
    if (p.name.startswith(".env") and p.name != ".env.example") or p.suffix in {".db", ".sqlite", ".pem"}:
        errors.append(f"runtime data/credential file tracked: {name}")
    data = (root / name).read_bytes()
    for label, pattern in patterns.items():
        if re.search(pattern, data):
            errors.append(f"possible {label} in {name}")

graph = json.loads((root / "third_party/licenses/MODULE_GRAPH.json").read_text())
checked = 0
for module in graph["modules"]:
    included = any(module.get(k, False) for k in ("runtime_linux", "runtime_darwin_arm64", "project_tests_darwin_arm64"))
    if included and not module["license_files"]:
        errors.append(f"included module has no license: {module['module']}")
    for item in module["license_files"]:
        p = root / item["path"]
        if not p.is_file() or hashlib.sha256(p.read_bytes()).hexdigest() != item["sha256"]:
            errors.append(f"license digest mismatch: {item['path']}")
        checked += 1
if errors:
    raise SystemExit("\n".join(errors))
print(f"Tracked-file credential checks passed; {checked} module license digests verified.")
