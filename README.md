# solco-gitkit

The Git and Files domain shared by [Solco](https://github.com/thewei/solco) (the macOS workspace for coding agents) and Solco Git (the standalone Git + Files app). Two packages, standard library only:

| Package | What it is |
|---|---|
| `gitworkbench` | The product's only Git runner (argv calls, timeouts, per-repository serialization, no automatic network fetch, no force push, no clean) and the model behind change snapshots, commits, branches, stashes, tags, worktrees, rebase/merge/cherry-pick, graph, blame and patches |
| `filesview` | The Files data layer: an ignore-aware tree, preview classification, search and the guarded create / rename / trash operations |

```sh
go test ./...
```

## Working next to Solco

Solco and Solco Git `require` this module at a tagged version and never `replace` it with a relative path, because their CI sees only its own repository. On a machine that has the repositories side by side, an uncommitted `go.work` in each of them (both files are gitignored) makes the local copy win:

```sh
cd ../solco && go work init . && go work edit -replace github.com/thewei/solco-gitkit=../solco-gitkit
```

This repository is public ([github.com/thewei/solco-gitkit](https://github.com/thewei/solco-gitkit), MIT) and `v0.1.0` is tagged, so CI in the other repositories resolves the module with no credential. A change reaches them by a new tag, then `GOWORK=off go get github.com/thewei/solco-gitkit@vX.Y.Z && GOWORK=off go mod tidy` in each of them.
