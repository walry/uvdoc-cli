package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Tencent/WeKnora/client"
	"github.com/spf13/cobra"
)

func newKnowledgeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "knowledge",
		Aliases: []string{"kn", "doc"},
		Short:   "知识（文档）管理",
		Long:    "知识条目的批量上传、查询、删除、重新解析、下载、迁移等操作。",
	}
	cmd.AddCommand(
		newUploadCommand(),
		newUploadURLCommand(),
		newKnowledgeListCommand(),
		newKnowledgeGetCommand(),
		newKnowledgeDeleteCommand(),
		newKnowledgeReparseCommand(),
		newKnowledgeCancelParseCommand(),
		newKnowledgeDownloadCommand(),
		newKnowledgeSpansCommand(),
		newManualCommand(),
		newKnowledgeMoveCommand(),
		newKnowledgeMoveProgressCommand(),
		newKnowledgeTagCommand(),
	)
	return cmd
}

func knowledgeHeaders() []string {
	return []string{"ID", "TITLE", "TYPE", "FILE_NAME", "SIZE", "PARSE_STATUS", "ENABLE_STATUS", "SOURCE", "CREATED"}
}

func knowledgeRow(k client.Knowledge) []string {
	return []string{
		k.ID,
		truncate(k.Title, 32),
		defaultString(k.Type, "-"),
		truncate(k.FileName, 32),
		formatSize(k.FileSize),
		defaultString(k.ParseStatus, "-"),
		defaultString(k.EnableStatus, "-"),
		truncate(k.Source, 28),
		formatTime(k.CreatedAt),
	}
}

func newKnowledgeListCommand() *cobra.Command {
	var (
		page        int
		pageSize    int
		all         bool
		tagID       string
		keyword     string
		fileType    string
		parseStatus string
		source      string
		startTime   string
		endTime     string
	)
	cmd := &cobra.Command{
		Use:     "list <knowledge-base-id>",
		Aliases: []string{"ls"},
		Short:   "列出知识库中的知识",
		Args:    cobra.ExactArgs(1),
		Example: `  uvdoc knowledge list kb-1
  uvdoc knowledge list kb-1 --keyword 手册 --parse-status completed -o json
  uvdoc knowledge list kb-1 --all --page-size 100`,
		RunE: func(cmd *cobra.Command, args []string) error {
			filter := client.KnowledgeListFilter{
				TagID:       tagID,
				Keyword:     keyword,
				FileType:    fileType,
				ParseStatus: parseStatus,
				Source:      source,
			}
			if startTime != "" {
				t, err := parseTime(startTime)
				if err != nil {
					return err
				}
				filter.StartTime = t
			}
			if endTime != "" {
				t, err := parseTime(endTime)
				if err != nil {
					return err
				}
				filter.EndTime = t
			}

			api := newClient(cmd)
			items := make([]client.Knowledge, 0, pageSize)
			total := int64(0)
			for p := page; ; p++ {
				batch, count, err := api.ListKnowledgeWithFilter(cmd.Context(), args[0], p, pageSize, filter)
				if err != nil {
					return err
				}
				items = append(items, batch...)
				total = count
				if !all || len(batch) == 0 || int64(len(items)) >= count {
					break
				}
			}

			if err := renderList(cmd, items, knowledgeHeaders(), knowledgeRow); err != nil {
				return err
			}
			footerf(cmd, "共 %d 条（当前返回 %d 条）\n", total, len(items))
			return nil
		},
	}
	cmd.Flags().IntVar(&page, "page", 1, "页码")
	cmd.Flags().IntVar(&pageSize, "page-size", 20, "每页条数")
	cmd.Flags().BoolVar(&all, "all", false, "自动翻页拉取全部结果")
	cmd.Flags().StringVar(&tagID, "tag-id", "", "按标签过滤")
	cmd.Flags().StringVar(&keyword, "keyword", "", "按标题关键字过滤")
	cmd.Flags().StringVar(&fileType, "file-type", "", "按文件类型过滤，例如 pdf")
	cmd.Flags().StringVar(&parseStatus, "parse-status", "", "按解析状态过滤，例如 pending/processing/completed/failed")
	cmd.Flags().StringVar(&source, "source", "", "按来源过滤")
	cmd.Flags().StringVar(&startTime, "start-time", "", "更新时间起始（RFC3339 或 2006-01-02）")
	cmd.Flags().StringVar(&endTime, "end-time", "", "更新时间截止（RFC3339 或 2006-01-02）")
	return cmd
}

func newKnowledgeGetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <knowledge-id> [knowledge-id...]",
		Short: "查看知识详情（多个 ID 时使用批量接口）",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			api := newClient(cmd)
			var items []client.Knowledge
			if len(args) == 1 {
				k, err := api.GetKnowledge(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				items = []client.Knowledge{*k}
			} else {
				batch, err := api.GetKnowledgeBatch(cmd.Context(), args)
				if err != nil {
					return err
				}
				items = batch
			}
			return renderList(cmd, items, knowledgeHeaders(), knowledgeRow)
		},
	}
	return cmd
}

func newKnowledgeDeleteCommand() *cobra.Command {
	var (
		yes         bool
		concurrency int
	)
	cmd := &cobra.Command{
		Use:   "delete <knowledge-id> [knowledge-id...]",
		Short: "批量删除知识（服务端异步执行）",
		Args:  cobra.MinimumNArgs(1),
		Example: `  uvdoc knowledge delete kn-1 kn-2 kn-3
  uvdoc knowledge list kb-1 -o json | jq -r '.[].id' | xargs uvdoc knowledge delete -y`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirm(cmd, yes, fmt.Sprintf("确认删除 %d 条知识？该操作不可恢复", len(args))); err != nil {
				return err
			}
			api := newClient(cmd)
			results := runBatch(cmd.Context(), args, concurrency, false, func(ctx context.Context, id string) (*client.Knowledge, error) {
				return nil, api.DeleteKnowledge(ctx, id)
			}, nil)
			if err := renderBatchResults(cmd, results); err != nil {
				return err
			}
			return batchError(results)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "跳过确认提示")
	cmd.Flags().IntVarP(&concurrency, "concurrency", "j", 4, "并发数")
	return cmd
}

func newKnowledgeReparseCommand() *cobra.Command {
	var concurrency int
	cmd := &cobra.Command{
		Use:   "reparse <knowledge-id> [knowledge-id...]",
		Short: "批量重新解析知识",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			api := newClient(cmd)
			results := runBatch(cmd.Context(), args, concurrency, false, func(ctx context.Context, id string) (*client.Knowledge, error) {
				return api.ReparseKnowledge(ctx, id)
			}, nil)
			if err := renderBatchResults(cmd, results); err != nil {
				return err
			}
			return batchError(results)
		},
	}
	cmd.Flags().IntVarP(&concurrency, "concurrency", "j", 4, "并发数")
	return cmd
}

func newKnowledgeCancelParseCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cancel-parse <knowledge-id>",
		Short: "取消正在进行的解析",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := newClient(cmd).CancelKnowledgeParse(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return renderOne(cmd, *k, knowledgeHeaders(), knowledgeRow)
		},
	}
	return cmd
}

func newKnowledgeDownloadCommand() *cobra.Command {
	var (
		out   string
		force bool
	)
	cmd := &cobra.Command{
		Use:   "download <knowledge-id> [--out PATH]",
		Short: "下载知识源文件",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, body, err := newClient(cmd).OpenKnowledgeFile(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			defer body.Close()

			dest := out
			if dest == "" {
				if name == "" {
					return fmt.Errorf("服务端未返回文件名，请使用 --out 指定保存路径")
				}
				dest = name
			}
			if info, err := os.Stat(dest); err == nil && info.IsDir() {
				dest = filepath.Join(dest, defaultString(name, args[0]))
			}
			if !force {
				if _, err := os.Stat(dest); err == nil {
					return fmt.Errorf("文件已存在: %s（使用 --force 覆盖）", dest)
				}
			}

			f, err := os.Create(dest)
			if err != nil {
				return fmt.Errorf("创建文件失败: %w", err)
			}
			if _, err := io.Copy(f, body); err != nil {
				_ = f.Close()
				_ = os.Remove(dest)
				return fmt.Errorf("写入文件失败: %w", err)
			}
			if err := f.Close(); err != nil {
				return err
			}
			return renderMessage(cmd, "下载完成", [][2]string{{"KNOWLEDGE_ID", args[0]}, {"PATH", dest}})
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "保存路径（文件或目录，默认使用服务端返回的文件名）")
	cmd.Flags().BoolVar(&force, "force", false, "覆盖已存在的文件")
	return cmd
}

func newKnowledgeSpansCommand() *cobra.Command {
	var attempt int
	cmd := &cobra.Command{
		Use:   "spans <knowledge-id>",
		Short: "查看文档解析阶段（span）耗时与状态",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			trace, err := newClient(cmd).GetKnowledgeProcessingSpans(cmd.Context(), args[0], attempt)
			if err != nil {
				return err
			}
			return renderSpans(cmd, trace)
		},
	}
	cmd.Flags().IntVar(&attempt, "attempt", 0, "解析尝试序号，0 表示最新一次")
	return cmd
}

func renderSpans(cmd *cobra.Command, trace *client.KnowledgeProcessingTrace) error {
	if isJSON(cmd) {
		return printJSON(cmd, trace)
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "knowledge_id: %s\nparse_status: %s\ncurrent_stage: %s\nattempt: %d\n",
		trace.KnowledgeID, trace.ParseStatus, trace.CurrentStage, trace.CurrentAttempt)
	if trace.LastError != nil {
		fmt.Fprintf(out, "last_error: [%s] %s %s\n", trace.LastError.Stage, trace.LastError.Code, trace.LastError.Message)
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "STAGE\tSTATUS\tDURATION\tERROR")
	if trace.Trace != nil {
		for _, child := range trace.Trace.Children {
			writeSpan(w, child, 0)
		}
	}
	_ = w.Flush()
	return nil
}

func writeSpan(w *tabwriter.Writer, node *client.KnowledgeSpanNode, depth int) {
	if node == nil {
		return
	}
	indent := strings.Repeat("  ", depth)
	errMsg := node.ErrorMessage
	if node.ErrorCode != "" {
		errMsg = node.ErrorCode + " " + errMsg
	}
	fmt.Fprintf(w, "%s%s\t%s\t%dms\t%s\n", indent, node.Name, node.Status, node.DurationMs, errMsg)
	for _, child := range node.Children {
		writeSpan(w, child, depth+1)
	}
}

func newManualCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "manual",
		Short: "手工录入的 Markdown 知识",
	}
	cmd.AddCommand(newManualCreateCommand(), newManualUpdateCommand())
	return cmd
}

func newManualCreateCommand() *cobra.Command {
	var (
		title       string
		content     string
		contentFile string
		tagID       string
		channel     string
	)
	cmd := &cobra.Command{
		Use:     "create <knowledge-base-id> --title TITLE (--content TEXT | --content-file FILE)",
		Short:   "创建手工 Markdown 知识",
		Args:    cobra.ExactArgs(1),
		Example: `  uvdoc knowledge manual create kb-1 --title "常见问题" --content-file ./faq.md`,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := readContent(content, contentFile)
			if err != nil {
				return err
			}
			k, err := newClient(cmd).CreateManualKnowledge(cmd.Context(), args[0], &client.CreateManualKnowledgeRequest{
				Title:   title,
				Content: body,
				TagID:   tagID,
				Channel: channel,
			})
			if err != nil {
				return err
			}
			return renderOne(cmd, *k, knowledgeHeaders(), knowledgeRow)
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "知识标题")
	cmd.Flags().StringVar(&content, "content", "", "Markdown 正文")
	cmd.Flags().StringVar(&contentFile, "content-file", "", "从文件读取 Markdown 正文")
	cmd.Flags().StringVar(&tagID, "tag-id", "", "关联标签 ID")
	cmd.Flags().StringVar(&channel, "channel", "", "录入渠道，留空使用服务端默认值")
	_ = cmd.MarkFlagRequired("title")
	return cmd
}

func newManualUpdateCommand() *cobra.Command {
	var (
		title       string
		content     string
		contentFile string
	)
	cmd := &cobra.Command{
		Use:   "update <knowledge-id> [--title TITLE] [--content TEXT | --content-file FILE]",
		Short: "更新手工 Markdown 知识",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &client.UpdateManualKnowledgeRequest{}
			if cmd.Flags().Changed("title") {
				req.Title = title
			}
			if cmd.Flags().Changed("content") || cmd.Flags().Changed("content-file") {
				body, err := readContent(content, contentFile)
				if err != nil {
					return err
				}
				req.Content = body
			}
			k, err := newClient(cmd).UpdateManualKnowledge(cmd.Context(), args[0], req)
			if err != nil {
				return err
			}
			return renderOne(cmd, *k, knowledgeHeaders(), knowledgeRow)
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "知识标题")
	cmd.Flags().StringVar(&content, "content", "", "Markdown 正文")
	cmd.Flags().StringVar(&contentFile, "content-file", "", "从文件读取 Markdown 正文")
	return cmd
}

// readContent 优先使用 --content-file，其次 --content
func readContent(content, contentFile string) (string, error) {
	if contentFile != "" {
		data, err := os.ReadFile(contentFile)
		if err != nil {
			return "", fmt.Errorf("读取文件失败: %w", err)
		}
		return string(data), nil
	}
	return content, nil
}

func newKnowledgeMoveCommand() *cobra.Command {
	var (
		ids         []string
		mode        string
		wait        bool
		interval    time.Duration
		waitTimeout time.Duration
	)
	cmd := &cobra.Command{
		Use:     "move <source-kb-id> <target-kb-id> --id <knowledge-id>",
		Short:   "把知识迁移到另一个知识库（异步任务）",
		Args:    cobra.ExactArgs(2),
		Example: `  uvdoc knowledge move kb-src kb-dst --id kn-1 --id kn-2 --mode reparse --wait`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(ids) == 0 {
				return fmt.Errorf("请至少通过 --id 指定一条知识")
			}
			if mode != "reuse_vectors" && mode != "reparse" {
				return fmt.Errorf("--mode 只能是 reuse_vectors 或 reparse")
			}
			api := newClient(cmd)
			result, err := api.MoveKnowledge(cmd.Context(), &client.MoveKnowledgeRequest{
				KnowledgeIDs: ids,
				SourceKBID:   args[0],
				TargetKBID:   args[1],
				Mode:         mode,
			})
			if err != nil {
				return err
			}
			pairs := [][2]string{
				{"TASK_ID", result.TaskID},
				{"SOURCE_KB_ID", result.SourceKBID},
				{"TARGET_KB_ID", result.TargetKBID},
				{"KNOWLEDGE_COUNT", fmt.Sprintf("%d", result.KnowledgeCount)},
			}
			if !wait {
				pairs = append(pairs, [2]string{"MESSAGE", result.Message})
				return renderMessage(cmd, "迁移任务已提交", pairs)
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), waitTimeout)
			defer cancel()
			last, err := pollLoop(ctx, cmd.OutOrStdout(), interval, func(ctx context.Context) (string, bool, error) {
				p, err := api.GetKnowledgeMoveProgress(ctx, result.TaskID)
				if err != nil {
					return "", false, err
				}
				status := fmt.Sprintf("status=%s progress=%d%% (%d/%d) %s", p.Status, p.Progress, p.Processed, p.Total, p.Message)
				switch p.Status {
				case "completed", "failed":
					return status, true, nil
				default:
					return status, false, nil
				}
			})
			pairs = append(pairs, [2]string{"LAST_STATUS", last})
			if err != nil {
				return fmt.Errorf("%w: %s", err, last)
			}
			return renderMessage(cmd, "迁移任务结束", pairs)
		},
	}
	cmd.Flags().StringArrayVar(&ids, "id", nil, "要迁移的知识 ID，可重复指定")
	cmd.Flags().StringVar(&mode, "mode", "reuse_vectors", "迁移模式: reuse_vectors（复用向量）| reparse（重新解析）")
	cmd.Flags().BoolVar(&wait, "wait", false, "等待任务完成并输出进度")
	cmd.Flags().DurationVar(&interval, "poll-interval", 3*time.Second, "轮询间隔（配合 --wait 使用）")
	cmd.Flags().DurationVar(&waitTimeout, "wait-timeout", 10*time.Minute, "等待超时时间（配合 --wait 使用）")
	return cmd
}

func newKnowledgeMoveProgressCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "move-progress <task-id>",
		Short: "查看知识迁移任务进度",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			progress, err := newClient(cmd).GetKnowledgeMoveProgress(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return renderPairs(cmd, [][2]string{
				{"TASK_ID", progress.TaskID},
				{"STATUS", progress.Status},
				{"PROGRESS", fmt.Sprintf("%d%%", progress.Progress)},
				{"PROCESSED", fmt.Sprintf("%d/%d", progress.Processed, progress.Total)},
				{"MESSAGE", progress.Message},
				{"ERROR", progress.Error},
			})
		},
	}
	return cmd
}

func newKnowledgeTagCommand() *cobra.Command {
	var (
		tagID string
		clear bool
	)
	cmd := &cobra.Command{
		Use:   "tag <knowledge-id> [knowledge-id...] (--tag-id ID | --clear)",
		Short: "批量设置或清空知识标签",
		Args:  cobra.MinimumNArgs(1),
		Example: `  uvdoc knowledge tag kn-1 kn-2 --tag-id tag-9
  uvdoc knowledge tag kn-1 --clear`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if clear && tagID != "" {
				return fmt.Errorf("--clear 与 --tag-id 不能同时使用")
			}
			if !clear && tagID == "" {
				return fmt.Errorf("请使用 --tag-id 指定标签，或使用 --clear 清空标签")
			}

			updates := make(map[string]*string, len(args))
			for _, id := range args {
				if clear {
					updates[id] = nil
					continue
				}
				t := tagID
				updates[id] = &t
			}
			if err := newClient(cmd).BatchUpdateKnowledgeTags(cmd.Context(), updates); err != nil {
				return err
			}
			action := "标签已更新"
			if clear {
				action = "标签已清空"
			}
			return renderMessage(cmd, action, [][2]string{
				{"COUNT", fmt.Sprintf("%d", len(args))},
				{"TAG_ID", defaultString(tagID, "-")},
			})
		},
	}
	cmd.Flags().StringVar(&tagID, "tag-id", "", "要设置的标签 ID")
	cmd.Flags().BoolVar(&clear, "clear", false, "清空标签")
	return cmd
}
