// Command uvdoc 是 WeKnora 知识库管理的命令行工具。
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"gitlab.innovsharing.com/ai_services/uvdoc-cli/cmd"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := cmd.Execute(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}
