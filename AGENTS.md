# AGENTS.md

Read `CLAUDE.md` first: it names the two packages and the rules a package here must keep.

## Checks

```sh
GOTOOLCHAIN=auto go vet ./...
GOTOOLCHAIN=auto go test ./...
GOTOOLCHAIN=auto go list -deps ./... | rg '^[^/]+\.[^/]+/' | rg -v '^github.com/thewei/solco-gitkit/'   # prints nothing
git diff --check
```

The tests run a real `git` binary against temporary repositories, so `git` must be on `PATH`.

## Scope

- This module is domain code shared by Solco and Solco Git. UI, MyGo, Herdr, and anything that needs a window belong in those apps.
- The Git runner policy lives in `gitworkbench/runner.go`; do not add a second way to run `git` and do not add network or credential prompts (`GIT_TERMINAL_PROMPT=0` stays).
- A behaviour change that the phone Host also shows (`filesview` backs its Files browse and search; `gitworkbench` backs its Git writes) lands together with the golden files in `solco` and `solco-mobile`.

## Git safety

Do not stage, commit, tag, push, reset or rewrite Git state unless asked. Publishing a version is a manual step: push `main`, tag `vX.Y.Z` (the tag is immutable once pushed), then bump the `require` in `solco` and `solco-git`.
