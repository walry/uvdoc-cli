// Package cmd 基于 cobra 封装 WeKnora 知识库管理相关的 API。
package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/client"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const (
	// envBaseURL 服务地址的环境变量名
	envBaseURL = "UVDOC_BASE_URL"
	// envAPIKey API Key 的环境变量名
	envAPIKey = "UVDOC_API_KEY"

	defaultBaseURL = "http://localhost:8080"
)

// opts 保存全局命令行选项
var opts struct {
	baseURL  string
	apiKey   string
	timeout  time.Duration
	output   string
	tenantID uint64
}

// NewRootCommand 构建根命令
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "uvdoc",
		Short: "WeKnora 知识库管理命令行工具",
		Long: `uvdoc 将 WeKnora 的知识库管理 API 封装为命令行子命令。

服务地址与密钥可通过 --base-url / --api-key 指定，
也可以分别使用环境变量 UVDOC_BASE_URL / UVDOC_API_KEY。
两者均未提供时，将在交互式终端中提示输入。

所有列表类命令默认输出表格，使用 -o json 可输出结构化 JSON 便于脚本处理。`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if opts.output != "table" && opts.output != "json" {
				return fmt.Errorf("不支持的输出格式 %q，可选值: table, json", opts.output)
			}
			if err := resolveCredentials(cmd); err != nil {
				return err
			}
			opts.baseURL = strings.TrimRight(opts.baseURL, "/")
			if opts.baseURL == "" {
				return fmt.Errorf("服务地址为空，请使用 --base-url 或环境变量 %s 指定", envBaseURL)
			}
			return nil
		},
	}

	flags := root.PersistentFlags()
	flags.StringVar(&opts.baseURL, "base-url", envOr(envBaseURL, defaultBaseURL), "WeKnora 服务地址")
	flags.StringVar(&opts.apiKey, "api-key", envOr(envAPIKey, ""), "API Key（X-API-Key 请求头）")
	flags.DurationVar(&opts.timeout, "timeout", 60*time.Second, "单个 HTTP 请求的超时时间")
	flags.StringVarP(&opts.output, "output", "o", "table", "输出格式: table | json")
	flags.Uint64Var(&opts.tenantID, "tenant-id", 0, "设置 X-Tenant-ID（仅跨租户管理员使用，默认不发送）")

	root.AddCommand(newKBCommand(), newKnowledgeCommand(), newPublishUploadCommand())
	return root
}

// Execute 运行根命令，错误由调用方打印
func Execute(ctx context.Context) error {
	return NewRootCommand().ExecuteContext(ctx)
}

// newClient 根据全局选项创建 API 客户端
func newClient(cmd *cobra.Command) *client.Client {
	options := []client.ClientOption{client.WithTimeout(opts.timeout)}
	if opts.apiKey != "" {
		options = append(options, client.WithAPIKey(opts.apiKey))
	}
	if opts.tenantID != 0 {
		options = append(options, client.WithTenantID(opts.tenantID))
	}
	return client.NewClient(opts.baseURL, options...)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// resolveCredentials 在命令参数与环境变量均未提供时，交互式提示用户输入
// 服务地址与 API Key。非交互式环境（如管道、CI）保持原有默认行为，不阻塞执行。
func resolveCredentials(cmd *cobra.Command) error {
	// help/completion/__complete 等本地命令无需访问服务，跳过提示
	if skipsCredentials(cmd) {
		return nil
	}

	baseProvided := cmd.Flags().Changed("base-url") || os.Getenv(envBaseURL) != ""
	keyProvided := cmd.Flags().Changed("api-key") || os.Getenv(envAPIKey) != ""
	if baseProvided && keyProvided {
		return nil
	}
	if !isInteractive(cmd) {
		return nil
	}

	reader := bufio.NewReader(cmd.InOrStdin())
	prompt := func(label string) (string, error) {
		fmt.Fprint(cmd.OutOrStdout(), label)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimSpace(line), nil
	}

	if !baseProvided {
		answer, err := prompt(fmt.Sprintf("请输入 WeKnora 服务地址 [%s]: ", defaultBaseURL))
		if err != nil {
			return err
		}
		if answer != "" {
			opts.baseURL = answer
		}
	}
	if !keyProvided {
		answer, err := prompt("请输入 API Key（直接回车表示不设置）: ")
		if err != nil {
			return err
		}
		opts.apiKey = answer
	}
	return nil
}

// skipsCredentials 判断命令是否为本地命令（help/completion 等），无需凭证。
func skipsCredentials(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "help", "completion", "__complete", "__completeNoDesc":
			return true
		}
	}
	return false
}

// isInteractive 判断命令的标准输入是否连接到一个交互式终端。
func isInteractive(cmd *cobra.Command) bool {
	f, ok := cmd.InOrStdin().(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
