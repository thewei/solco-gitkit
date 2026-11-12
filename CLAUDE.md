# Solco Gitkit

The shared Git and Files domain of the Solco family: `solco` (the macOS workspace for coding agents) and `solco-git` (the standalone Git + Files app) both `require` this module. The root module is standard library only; no UI, no Herdr, no process lifecycle. `engine/` is a nested module (own `go.mod`) and the only place that imports LazyGit.

<directory>
gitworkbench/ - the one Git runner of the product: argv-only `git` calls with timeouts and per-repository serialization, change snapshots, commit/branch/stash/tag/worktree/sequencer operations, graph, blame, patches (see gitworkbench/CLAUDE.md)
engine/ - nested Go module `github.com/thewei/solco-gitkit/engine` (2026-11-12 decision): LazyGit's command layer behind `gitworkbench.Runner.Backend`, with the runner policy re-imposed (policy.go); capabilities move onto it one group at a time, each proven by the same real-repository test on both paths (see engine/CLAUDE.md)
filesview/ - Files data layer: ignore-aware tree, flavor/preview classification, search, and the guarded file mutations (create/rename/trash); also behind the phone Host's Files browse and search
</directory>

<config>
go.mod - module github.com/thewei/solco-gitkit, Go 1.27.3, no third-party dependencies (the nested `engine/go.mod` carries LazyGit's; the root's `go list -deps ./...` never sees it) (`go list -deps ./...` lists the standard library and this module only; `.github/workflows/checks.yml` enforces it)
.github/workflows/checks.yml - `go vet` + `go test` on macOS and Linux
</config>

Rules: a package here never imports MyGo, Herdr, or anything from `solco`/`solco-git`; the runner policy (argv calls only, `GIT_TERMINAL_PROMPT=0`, read 5 s / write 30 s, no automatic network fetch, no force push, no clean) is part of the contract and changes only by a recorded product decision in `solco`; log operation names, counts and errors, never paths, commit messages, diffs or credentials; tests use a real `git` binary in temporary repositories.

[PROTOCOL]: 变更时更新此头部，然后检查子目录 CLAUDE.md
