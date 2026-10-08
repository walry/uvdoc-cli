module gitlab.innovsharing.com/ai_services/uvdoc-cli

go 1.26.3

require (
	github.com/Tencent/WeKnora/client v0.0.0
	github.com/spf13/cobra v1.10.2
	golang.org/x/term v0.33.0
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	golang.org/x/sys v0.34.0 // indirect
)

replace github.com/Tencent/WeKnora/client => ./client
