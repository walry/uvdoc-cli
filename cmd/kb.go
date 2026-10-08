package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/client"
	"github.com/spf13/cobra"
)

func newKBCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "kb",
		Aliases: []string{"knowledge-base", "knowledgebase"},
		Short:   "知识库管理",
		Long:    "知识库的创建、查询、更新、删除，以及清空、置顶、复制等操作。",
	}
	cmd.AddCommand(
		newKBCreateCommand(),
		newKBListCommand(),
		newKBGetCommand(),
		newKBUpdateCommand(),
		newKBDeleteCommand(),
		newKBClearCommand(),
		newKBPinCommand(),
		newKBCopyCommand(),
		newKBCopyProgressCommand(),
		newKBMoveTargetsCommand(),
	)
	return cmd
}

func kbHeaders() []string {
	return []string{"ID", "NAME", "TYPE", "DESCRIPTION", "KNOWLEDGE", "CHUNKS", "PINNED", "CREATED"}
}

func kbRow(kb client.KnowledgeBase) []string {
	return []string{
		kb.ID,
		kb.Name,
		defaultString(kb.Type, "-"),
		truncate(kb.Description, 40),
		formatInt64(kb.KnowledgeCount),
		formatInt64(kb.ChunkCount),
		formatBool(kb.IsPinned),
		formatTime(kb.CreatedAt),
	}
}

// defaultModelIDs 从服务端模型列表中解析创建知识库所需的默认模型：
// 分别取 Embedding 类型与对话（KnowledgeQA）类型列表中的第一个，
// 对应类型没有模型时返回空字符串。
func defaultModelIDs(ctx context.Context, api *client.Client) (embeddingModelID, chatModelID string, err error) {
	models, err := api.ListModels(ctx)
	if err != nil {
		return "", "", err
	}
	for _, m := range models {
		if embeddingModelID == "" && m.Type == client.ModelTypeEmbedding {
			embeddingModelID = m.ID
		}
		if chatModelID == "" && m.Type == client.ModelTypeKnowledgeQA {
			chatModelID = m.ID
		}
		if embeddingModelID != "" && chatModelID != "" {
			break
		}
	}
	return embeddingModelID, chatModelID, nil
}

func newKBCreateCommand() *cobra.Command {
	var (
		name             string
		description      string
		kbType           string
		chunkSize        int
		chunkOverlap     int
		separators       []string
		embeddingModelID string
		summaryModelID   string
		imageModelID     string
	)
	cmd := &cobra.Command{
		Use:   "create --name NAME",
		Short: "创建知识库",
		Example: `  # 使用默认分块配置创建
  uvdoc kb create --name 产品文档

  # 指定分块与模型
  uvdoc kb create --name 产品文档 --description "产品使用手册" \
    --chunk-size 512 --chunk-overlap 50 --embedding-model-id emb-1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			api := newClient(cmd)
			// 未显式指定模型时，用服务端模型列表中的默认项兜底。
			if embeddingModelID == "" || summaryModelID == "" {
				defEmbedding, defChat, err := defaultModelIDs(cmd.Context(), api)
				if err != nil {
					return err
				}
				if embeddingModelID == "" {
					embeddingModelID = defEmbedding
				}
				if summaryModelID == "" {
					summaryModelID = defChat
				}
			}
			kb := &client.KnowledgeBase{
				Name:                  name,
				Description:           description,
				Type:                  kbType,
				ChunkingConfig:        client.ChunkingConfig{ChunkSize: chunkSize, ChunkOverlap: chunkOverlap, Separators: separators},
				ImageProcessingConfig: client.ImageProcessingConfig{ModelID: imageModelID},
				EmbeddingModelID:      embeddingModelID,
				SummaryModelID:        summaryModelID,
			}
			created, err := api.CreateKnowledgeBase(cmd.Context(), kb)
			if err != nil {
				return err
			}
			return renderOne(cmd, *created, kbHeaders(), kbRow)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "知识库名称（必填，同一租户内唯一）")
	cmd.Flags().StringVar(&description, "description", "", "知识库描述")
	cmd.Flags().StringVar(&kbType, "type", "", "知识库类型（留空由服务端决定）")
	cmd.Flags().IntVar(&chunkSize, "chunk-size", 512, "分块大小")
	cmd.Flags().IntVar(&chunkOverlap, "chunk-overlap", 50, "分块重叠大小")
	cmd.Flags().StringSliceVar(&separators, "separators", []string{"\n\n", "\n", ". ", "? ", "! "}, "分块分隔符")
	cmd.Flags().StringVar(&embeddingModelID, "embedding-model-id", "", "Embedding 模型 ID")
	cmd.Flags().StringVar(&summaryModelID, "summary-model-id", "", "摘要模型 ID")
	cmd.Flags().StringVar(&imageModelID, "image-model-id", "", "图片处理（多模态）模型 ID")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newKBListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "列出所有知识库",
		RunE: func(cmd *cobra.Command, args []string) error {
			bases, err := newClient(cmd).ListKnowledgeBases(cmd.Context())
			if err != nil {
				return err
			}
			if err := renderList(cmd, bases, kbHeaders(), kbRow); err != nil {
				return err
			}
			footerf(cmd, "共 %d 个知识库\n", len(bases))
			return nil
		},
	}
	return cmd
}

func newKBGetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <knowledge-base-id>",
		Short: "查看知识库详情",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kb, err := newClient(cmd).GetKnowledgeBase(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return renderPairs(cmd, [][2]string{
				{"ID", kb.ID},
				{"NAME", kb.Name},
				{"TYPE", defaultString(kb.Type, "-")},
				{"DESCRIPTION", kb.Description},
				{"KNOWLEDGE_COUNT", formatInt64(kb.KnowledgeCount)},
				{"CHUNK_COUNT", formatInt64(kb.ChunkCount)},
				{"PROCESSING_COUNT", formatInt64(kb.ProcessingCount)},
				{"IS_PROCESSING", formatBool(kb.IsProcessing)},
				{"IS_PINNED", formatBool(kb.IsPinned)},
				{"EMBEDDING_MODEL_ID", defaultString(kb.EmbeddingModelID, "-")},
				{"SUMMARY_MODEL_ID", defaultString(kb.SummaryModelID, "-")},
				{"IMAGE_MODEL_ID", defaultString(kb.ImageProcessingConfig.ModelID, "-")},
				{"CHUNK_SIZE", fmt.Sprintf("%d", kb.ChunkingConfig.ChunkSize)},
				{"CHUNK_OVERLAP", fmt.Sprintf("%d", kb.ChunkingConfig.ChunkOverlap)},
				{"SEPARATORS", strings.Join(kb.ChunkingConfig.Separators, " | ")},
				{"CREATED_AT", formatTime(kb.CreatedAt)},
				{"UPDATED_AT", formatTime(kb.UpdatedAt)},
			})
		},
	}
	return cmd
}

func newKBUpdateCommand() *cobra.Command {
	var (
		name         string
		description  string
		chunkSize    int
		chunkOverlap int
		separators   []string
		imageModelID string
	)
	cmd := &cobra.Command{
		Use:   "update <knowledge-base-id>",
		Short: "更新知识库（未指定的字段保持原值）",
		Args:  cobra.ExactArgs(1),
		Example: `  uvdoc kb update kb-1 --description "新的描述"
  uvdoc kb update kb-1 --chunk-size 1024 --chunk-overlap 100`,
		RunE: func(cmd *cobra.Command, args []string) error {
			api := newClient(cmd)
			current, err := api.GetKnowledgeBase(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			req := &client.UpdateKnowledgeBaseRequest{
				Name:        current.Name,
				Description: current.Description,
			}
			if cmd.Flags().Changed("name") {
				req.Name = name
			}
			if cmd.Flags().Changed("description") {
				req.Description = description
			}

			configChanged := cmd.Flags().Changed("chunk-size") || cmd.Flags().Changed("chunk-overlap") ||
				cmd.Flags().Changed("separators") || cmd.Flags().Changed("image-model-id")
			if configChanged {
				chunking := current.ChunkingConfig
				if cmd.Flags().Changed("chunk-size") {
					chunking.ChunkSize = chunkSize
				}
				if cmd.Flags().Changed("chunk-overlap") {
					chunking.ChunkOverlap = chunkOverlap
				}
				if cmd.Flags().Changed("separators") {
					chunking.Separators = separators
				}
				image := current.ImageProcessingConfig
				if cmd.Flags().Changed("image-model-id") {
					image.ModelID = imageModelID
				}
				req.Config = &client.KnowledgeBaseConfig{
					ChunkingConfig:        chunking,
					ImageProcessingConfig: image,
				}
			}

			updated, err := api.UpdateKnowledgeBase(cmd.Context(), args[0], req)
			if err != nil {
				return err
			}
			return renderOne(cmd, *updated, kbHeaders(), kbRow)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "知识库名称")
	cmd.Flags().StringVar(&description, "description", "", "知识库描述")
	cmd.Flags().IntVar(&chunkSize, "chunk-size", 0, "分块大小")
	cmd.Flags().IntVar(&chunkOverlap, "chunk-overlap", 0, "分块重叠大小")
	cmd.Flags().StringSliceVar(&separators, "separators", nil, "分块分隔符")
	cmd.Flags().StringVar(&imageModelID, "image-model-id", "", "图片处理（多模态）模型 ID")
	return cmd
}

func newKBDeleteCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <knowledge-base-id>",
		Short: "删除知识库（连同其中的知识）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirm(cmd, yes, fmt.Sprintf("确认删除知识库 %s？该操作不可恢复", args[0])); err != nil {
				return err
			}
			if err := newClient(cmd).DeleteKnowledgeBase(cmd.Context(), args[0]); err != nil {
				return err
			}
			return renderMessage(cmd, "知识库已删除", [][2]string{{"ID", args[0]}})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "跳过确认提示")
	return cmd
}

func newKBClearCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "clear <knowledge-base-id>",
		Short: "清空知识库内容（保留知识库本身，服务端异步执行）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirm(cmd, yes, fmt.Sprintf("确认清空知识库 %s 的全部知识？", args[0])); err != nil {
				return err
			}
			result, err := newClient(cmd).ClearKnowledgeBaseContents(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return renderMessage(cmd, "清空任务已提交", [][2]string{
				{"ID", args[0]},
				{"DELETED_COUNT", fmt.Sprintf("%d", result.DeletedCount)},
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "跳过确认提示")
	return cmd
}

func newKBPinCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pin <knowledge-base-id>",
		Short: "切换知识库置顶状态",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kb, err := newClient(cmd).TogglePinKnowledgeBase(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return renderOne(cmd, *kb, kbHeaders(), kbRow)
		},
	}
	return cmd
}

func newKBCopyCommand() *cobra.Command {
	var (
		wait        bool
		interval    time.Duration
		waitTimeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "copy <source-id> <target-id>",
		Short: "复制知识库（异步任务）",
		Args:  cobra.ExactArgs(2),
		Example: `  uvdoc kb copy kb-src kb-dst
  uvdoc kb copy kb-src kb-dst --wait   # 等待复制完成`,
		RunE: func(cmd *cobra.Command, args []string) error {
			api := newClient(cmd)
			result, err := api.CopyKnowledgeBase(cmd.Context(), &client.CopyKnowledgeBaseRequest{
				SourceID: args[0],
				TargetID: args[1],
			})
			if err != nil {
				return err
			}
			if !wait {
				return renderMessage(cmd, "复制任务已提交", [][2]string{
					{"TASK_ID", result.TaskID},
					{"SOURCE_ID", result.SourceID},
					{"TARGET_ID", result.TargetID},
					{"MESSAGE", result.Message},
				})
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), waitTimeout)
			defer cancel()
			pairs := [][2]string{
				{"TASK_ID", result.TaskID},
				{"SOURCE_ID", result.SourceID},
				{"TARGET_ID", result.TargetID},
			}
			last, err := pollLoop(ctx, cmd.OutOrStdout(), interval, func(ctx context.Context) (string, bool, error) {
				p, err := api.GetKBCloneProgress(ctx, result.TaskID)
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
			return renderMessage(cmd, "复制任务结束", pairs)
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "等待任务完成并输出进度")
	cmd.Flags().DurationVar(&interval, "poll-interval", 3*time.Second, "轮询间隔（配合 --wait 使用）")
	cmd.Flags().DurationVar(&waitTimeout, "wait-timeout", 10*time.Minute, "等待超时时间（配合 --wait 使用）")
	return cmd
}

func newKBCopyProgressCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "copy-progress <task-id>",
		Short: "查看知识库复制任务进度",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			progress, err := newClient(cmd).GetKBCloneProgress(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return renderPairs(cmd, [][2]string{
				{"TASK_ID", progress.TaskID},
				{"STATUS", progress.Status},
				{"PROGRESS", fmt.Sprintf("%d%%", progress.Progress)},
				{"PROCESSED", fmt.Sprintf("%d/%d", progress.Processed, progress.Total)},
				{"SOURCE_ID", progress.SourceID},
				{"TARGET_ID", progress.TargetID},
				{"MESSAGE", progress.Message},
				{"ERROR", progress.Error},
			})
		},
	}
	return cmd
}

func newKBMoveTargetsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "move-targets <knowledge-base-id>",
		Short: "列出可作为知识迁移目标的知识库",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, err := newClient(cmd).ListMoveTargets(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return renderList(cmd, targets, kbHeaders(), kbRow)
		},
	}
	return cmd
}
