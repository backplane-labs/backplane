"""Select CI suites from a Git diff; unknown inputs conservatively select all."""

import os
import subprocess


def classify(paths):
    selected = set()
    all_suites = {"go", "web", "contracts", "compose"}
    for path in paths:
        if path.endswith(".md") or path in {"LICENSE", ".gitignore", ".gitattributes"}:
            continue
        if path.startswith((".claude/", ".idea/")):
            continue
        if path.startswith(".github/assets/"):
            continue
        # Documentation has its own path-filtered build and strict link checks.
        if path.startswith("website/") or path == ".github/workflows/docs.yml":
            continue
        if (
            path.endswith((".proto", ".sql"))
            or path in {"easyp.yaml", "easyp.lock", "sqld.yaml"}
            or path.endswith("/easyp.yaml")
            or path.startswith(("backplanepb/", "internal/store/db/", "web/packages/api/"))
            or path in {"web/scripts/api-index.mjs", "web/scripts/api-reference.mjs"}
        ):
            selected.update({"go", "web", "contracts"})
        elif path.startswith("web/"):
            selected.add("web")
        elif path.startswith(("docker-compose", "deployments/")) or path in {
            ".dockerignore", ".github/workflows/release.yml",
            "scripts/build-release.sh", "scripts/release-notes.py",
            "scripts/stamp-release.mjs", "scripts/stamp-release.test.mjs",
        }:
            selected.add("compose")
        elif path.startswith(("pkg/", "internal/", "cmd/", "examples/", "conformance/")) or path in {
            "go.mod", "go.sum", ".golangci.yaml",
        }:
            selected.add("go")
        else:
            selected.update(all_suites)
    return {suite: suite in selected for suite in sorted(all_suites)}


def changed_paths(base, head):
    # A branch's first push has an all-zero before SHA.
    if not base or set(base) == {"0"}:
        base = subprocess.check_output(
            ["git", "hash-object", "-t", "tree", "--stdin"], input=b""
        ).decode().strip()
    return subprocess.check_output(
        ["git", "diff", "--name-only", "--no-renames", "-z", base, head, "--"]
    ).decode().split("\0")[:-1]


if __name__ == "__main__":
    base, head = os.environ["CI_BASE"], os.environ["CI_HEAD"]
    if os.environ.get("CI_EVENT") == "pull_request":
        base = subprocess.check_output(["git", "merge-base", base, head]).decode().strip()
    result = classify(changed_paths(base, head))
    output = "".join(f"{key}={str(value).lower()}\n" for key, value in result.items())
    print(output, end="")
    with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as stream:
        stream.write(output)
