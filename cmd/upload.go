package cmd

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/Tencent/WeKnora/client"
	"github.com/spf13/cobra"
)

func newUploadCommand() *cobra.Command {
	var (
		recursive        bool
		exts             []string
		concurrency      int
		metadata         []string
		enableMultimodel bool
		channel          string
		failFast         bool
		dryRun           bool
	)
	cmd := &cobra.Command{
		Use:   "upload <knowledge-base-id> <path> [path...]",
		Short: "批量上传本地文件或目录到知识库",
		Args:  cobra.MinimumNArgs(2),
		Example: `  # 上传多个文件
  uvdoc knowledge upload kb-1 ./a.pdf ./b.docx

  # 递归上传目录下所有 PDF / Markdown，8 并发，附加元数据
  uvdoc knowledge upload kb-1 ./docs -r --ext pdf,md -j 8 --metadata source=manual

  # 输出 JSON 结果，便于脚本消费
  uvdoc knowledge upload kb-1 ./docs -r -o json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			knowledgeBaseID := args[0]
			files, err := collectFiles(args[1:], recursive, exts)
			if err != nil {
				return err
			}
			if len(files) == 0 {
				return fmt.Errorf("没有匹配到待上传的文件")
			}
			if dryRun {
				return renderDryRun(cmd, files)
			}

			meta, err := parseMetadata(metadata)
			if err != nil {
				return err
			}
			var multimodel *bool
			if cmd.Flags().Changed("enable-multimodel") {
				multimodel = &enableMultimodel
			}

			api := newClient(cmd)
			results := runBatch(cmd.Context(), files, concurrency, failFast,
				func(ctx context.Context, path string) (*client.Knowledge, error) {
					return api.CreateKnowledgeFromFile(ctx, knowledgeBaseID, path, meta, multimodel, "", channel, nil)
				}, progressFunc(cmd, len(files)))

			if err := renderBatchResults(cmd, results); err != nil {
				return err
			}
			ok, duplicate, failed := countResults(results)
			footerf(cmd, "成功 %d，重复跳过 %d，失败 %d，合计 %d\n", ok, duplicate, failed, len(results))
			return batchError(results)
		},
	}
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "递归扫描目录")
	cmd.Flags().StringSliceVar(&exts, "ext", nil, "按扩展名过滤，例如 pdf,docx（默认不过滤）")
	cmd.Flags().IntVarP(&concurrency, "concurrency", "j", 4, "并发上传数")
	cmd.Flags().StringArrayVar(&metadata, "metadata", nil, "附加元数据，格式 key=value，可重复指定")
	cmd.Flags().BoolVar(&enableMultimodel, "enable-multimodel", false, "启用多模态处理（不指定时使用知识库默认配置）")
	cmd.Flags().StringVar(&channel, "channel", "", "录入渠道，例如 api（留空使用服务端默认值）")
	cmd.Flags().BoolVar(&failFast, "fail-fast", false, "遇到第一个失败立即停止")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "只列出待上传文件，不实际上传")
	return cmd
}

func newUploadURLCommand() *cobra.Command {
	var (
		concurrency      int
		fileName         string
		fileType         string
		title            string
		tagID            string
		channel          string
		enableMultimodel bool
		failFast         bool
		dryRun           bool
	)
	cmd := &cobra.Command{
		Use:   "upload-url <knowledge-base-id> <url> [url...]",
		Short: "批量从 URL 创建知识（网页抓取或文件下载）",
		Args:  cobra.MinimumNArgs(2),
		Example: `  # 批量抓取网页
  uvdoc knowledge upload-url kb-1 https://example.com/a https://example.com/b

  # 下载 PDF 文件（显式指定文件名/类型，服务端按文件下载处理）
  uvdoc knowledge upload-url kb-1 https://example.com/report --file-name report.pdf`,
		RunE: func(cmd *cobra.Command, args []string) error {
			knowledgeBaseID := args[0]
			urls := args[1:]
			if dryRun {
				return renderDryRun(cmd, urls)
			}

			var multimodel *bool
			if cmd.Flags().Changed("enable-multimodel") {
				multimodel = &enableMultimodel
			}

			api := newClient(cmd)
			results := runBatch(cmd.Context(), urls, concurrency, failFast,
				func(ctx context.Context, rawURL string) (*client.Knowledge, error) {
					req := client.CreateKnowledgeFromURLRequest{
						URL:              rawURL,
						EnableMultimodel: multimodel,
						TagID:            tagID,
						Channel:          channel,
					}
					if cmd.Flags().Changed("file-name") {
						req.FileName = fileName
					}
					if cmd.Flags().Changed("file-type") {
						req.FileType = fileType
					}
					if cmd.Flags().Changed("title") {
						req.Title = title
					}
					return api.CreateKnowledgeFromURL(ctx, knowledgeBaseID, req)
				}, progressFunc(cmd, len(urls)))

			if err := renderBatchResults(cmd, results); err != nil {
				return err
			}
			ok, duplicate, failed := countResults(results)
			footerf(cmd, "成功 %d，重复跳过 %d，失败 %d，合计 %d\n", ok, duplicate, failed, len(results))
			return batchError(results)
		},
	}
	cmd.Flags().IntVarP(&concurrency, "concurrency", "j", 4, "并发数")
	cmd.Flags().StringVar(&fileName, "file-name", "", "文件名（用于提示服务端按文件下载处理）")
	cmd.Flags().StringVar(&fileType, "file-type", "", "文件类型，例如 pdf")
	cmd.Flags().StringVar(&title, "title", "", "知识标题")
	cmd.Flags().StringVar(&tagID, "tag-id", "", "关联标签 ID")
	cmd.Flags().StringVar(&channel, "channel", "", "录入渠道，留空使用服务端默认值")
	cmd.Flags().BoolVar(&enableMultimodel, "enable-multimodel", false, "启用多模态处理")
	cmd.Flags().BoolVar(&failFast, "fail-fast", false, "遇到第一个失败立即停止")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "只列出待上传 URL，不实际提交")
	return cmd
}

// progressFunc 输出实时进度到 stderr，JSON 模式下不输出以免污染结果
func progressFunc(cmd *cobra.Command, total int) func(int, batchResult) {
	if isJSON(cmd) {
		return nil
	}
	var done int64
	return func(idx int, r batchResult) {
		n := atomic.AddInt64(&done, 1)
		fmt.Fprintf(cmd.ErrOrStderr(), "[%d/%d] %s\t%s\n", n, total, r.Status, r.Item)
	}
}

func renderDryRun(cmd *cobra.Command, items []string) error {
	if isJSON(cmd) {
		return printJSON(cmd, items)
	}
	for _, item := range items {
		fmt.Fprintln(cmd.OutOrStdout(), item)
	}
	footerf(cmd, "合计 %d 个条目（--dry-run，未上传）\n", len(items))
	return nil
}

func parseMetadata(kvs []string) (map[string]string, error) {
	if len(kvs) == 0 {
		return nil, nil
	}
	meta := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("元数据格式应为 key=value，收到 %q", kv)
		}
		meta[key] = value
	}
	return meta, nil
}
