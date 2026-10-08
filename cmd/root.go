// Package cmd 基于 cobra 封装 WeKnora 知识库管理相关的 API。
package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/client"
	"github.com/spf13/cobra"
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

所有列表类命令默认输出表格，使用 -o json 可输出结构化 JSON 便于脚本处理。`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if opts.output != "table" && opts.output != "json" {
				return fmt.Errorf("不支持的输出格式 %q，可选值: table, json", opts.output)
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
