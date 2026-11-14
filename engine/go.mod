module github.com/thewei/solco-gitkit/engine

go 1.27.3

require (
	github.com/jesseduffield/lazygit v0.66.4-0.20261110130621-8290cc9357ad
	github.com/sirupsen/logrus v1.10.2
	github.com/stefanhaller/git-todo-parser v0.0.7-0.20250905083220-c50528f08304
	github.com/thewei/solco-gitkit v0.3.0
)

require (
	dario.cat/mergo v1.0.2 // indirect
	github.com/adrg/xdg v0.5.3 // indirect
	github.com/atotto/clipboard v0.1.4 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/cli/go-gh/v2 v2.16.2 // indirect
	github.com/cli/safeexec v1.0.1 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/cloudfoundry/jibber_jabber v0.0.0-20151120183258-bcc4c8345a21 // indirect
	github.com/creack/pty v1.1.24 // indirect
	github.com/gdamore/encoding v1.0.1 // indirect
	github.com/gdamore/tcell/v3 v3.6.0 // indirect
	github.com/go-errors/errors v1.5.1 // indirect
	github.com/gookit/color v1.6.1 // indirect
	github.com/jesseduffield/generics v0.0.0-20250517122708-b0b4a53a6f5c // indirect
	github.com/karimkhaleel/jsonschema v0.0.0-20231001195015-d933f0d94ea3 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/mgutz/str v1.2.0 // indirect
	github.com/petermattis/goid v0.0.0-20250813065127-a731cc31b4fe // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/sahilm/fuzzy v0.1.3 // indirect
	github.com/samber/lo v1.53.0 // indirect
	github.com/sasha-s/go-deadlock v0.3.9 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/wk8/go-ordered-map/v2 v2.1.8 // indirect
	github.com/xo/terminfo v1.2.0 // indirect
	golang.org/x/exp v0.0.0-20240719175910-8a7402abbf56 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/term v0.46.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// Local development inside this repository: the root module next door. A
// consumer's own go.mod decides which root version it gets; this line only
// serves `go test` here.
replace github.com/thewei/solco-gitkit => ../
