package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/client"
	"github.com/spf13/cobra"
)

// newUploadRetryCommand 构建 `upload retry`：只重试状态文件中记录的上传失败项。
func newUploadRetryCommand() *cobra.Command {
	var (
		stateFile    string
		product      string
		release      string
		docDir       string
		retry        int
		concurrency  int
		waitParse    bool
		pollInterval time.Duration
		parseTimeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "retry",
		Short: "重试状态文件中记录的上传失败项",
		Long: `retry 读取 upload 生成的状态文件，只重试其中 status=failed 的文档。

重试时会重新计算本地文件哈希并与知识库中的同名文档比对，
因此即使知识库状态已变化（例如失败项已被其它流程修复）也不会重复上传。
--state-file 缺省时按 --product/--release（或环境变量 PRODUCT/RELEASE_NAME）
推导为 .uvdoc/upload-<知识库>.json。`,
		Example: `  # 重试默认状态文件中的失败项
  uvdoc upload retry --product unipro --release v1.0

  # 显式指定状态文件，失败重试 3 次
  uvdoc upload retry --state-file .uvdoc/upload-unipro_v1.0.json --retry 3`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := stateFile
			if path == "" {
				p, r := strings.TrimSpace(product), strings.TrimSpace(release)
				if p == "" || r == "" {
					return fmt.Errorf("请使用 --state-file 指定状态文件，或使用 --product/--release（环境变量 %s/%s）推导默认路径",
						envProduct, envRelease)
				}
				path = filepath.Join(".uvdoc", "upload-"+sanitizeFileName(p+"_"+r)+".json")
			}
			if retry < 0 {
				return fmt.Errorf("--retry 不能为负数")
			}
			if concurrency < 1 {
				return fmt.Errorf("--concurrency 必须大于 0")
			}
			if waitParse && pollInterval <= 0 {
				return fmt.Errorf("--poll-interval 必须大于 0")
			}
			if waitParse && parseTimeout <= 0 {
				return fmt.Errorf("--parse-timeout 必须大于 0")
			}
			return runUploadRetry(cmd, path, docDir, retry, concurrency, waitParse, pollInterval, parseTimeout)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&stateFile, "state-file", "", "状态文件路径（默认按 --product/--release 推导）")
	flags.StringVar(&product, "product", envOr(envProduct, ""), "产品名称（环境变量 PRODUCT）")
	flags.StringVar(&release, "release", envOr(envRelease, ""), "发布版本（环境变量 RELEASE_NAME）")
	flags.StringVar(&docDir, "doc-dir", envOr(envDocDir, ""), "文档根目录，用于相对路径失效时定位文件（环境变量 DOC_DIR）")
	flags.IntVar(&retry, "retry", 2, "失败文档的重试次数")
	flags.IntVarP(&concurrency, "concurrency", "j", 4, "并发上传数")
	flags.BoolVar(&waitParse, "wait-parse", true, "上传后等待文档解析完成再返回（关闭后仅确认上传成功）")
	flags.DurationVar(&pollInterval, "poll-interval", 2*time.Second, "解析状态轮询间隔（配合 --wait-parse）")
	flags.DurationVar(&parseTimeout, "parse-timeout", 10*time.Minute, "单个文档解析等待超时（配合 --wait-parse）")
	return cmd
}

func runUploadRetry(cmd *cobra.Command, statePath, docDir string, retry, concurrency int,
	waitParse bool, pollInterval, parseTimeout time.Duration,
) error {
	ctx := cmd.Context()

	state, err := loadUploadState(statePath)
	if err != nil {
		return err
	}

	failedIdx := make([]int, 0)
	for i, r := range state.Results {
		if r.Status == statusFailed {
			failedIdx = append(failedIdx, i)
		}
	}
	if len(failedIdx) == 0 {
		return renderMessage(cmd, "没有需要重试的失败项", [][2]string{{"STATE_FILE", statePath}})
	}

	api := newClient(cmd)

	// 知识库优先使用状态中记录的 ID；缺失时按名称重新定位。
	kbID := state.KnowledgeBaseID
	if kbID == "" {
		kb, _, err := ensureKnowledgeBase(ctx, api, state.KnowledgeBase)
		if err != nil {
			return err
		}
		kbID = kb.ID
	}

	existing, err := listAllKnowledge(ctx, api, kbID)
	if err != nil {
		return err
	}
	byName := make(map[string]client.Knowledge)
	for _, k := range existing {
		if k.Type != "file" || k.FileName == "" {
			continue
		}
		if _, ok := byName[k.FileName]; !ok {
			byName[k.FileName] = k
		}
	}

	updates := make(map[int]uploadRecord, len(failedIdx))
	taskByPath := make(map[string]*uploadTask)
	idxByPath := make(map[string]int)
	var uploadPaths []string
	idxByID := make(map[string]int)
	var deleteIDs []string

	for _, i := range failedIdx {
		rec := state.Results[i]
		name := rec.FileName
		if name == "" {
			name = filepath.Base(rec.Path)
		}

		// 删除动作的重试：直接按记录的知识 ID 再次删除。
		if rec.Action == actionDelete {
			deleteIDs = append(deleteIDs, rec.Path)
			idxByID[rec.Path] = i
			continue
		}

		path := locateLocalFile(rec.Path, docDir, name)
		if path == "" {
			updates[i] = uploadRecord{
				Path: rec.Path, FileName: name, Hash: rec.Hash, Action: rec.Action,
				Status: statusFailed, Error: "本地文件不存在: " + rec.Path,
			}
			continue
		}
		hash, err := fileMD5(path)
		if err != nil {
			updates[i] = uploadRecord{
				Path: path, FileName: name, Action: rec.Action,
				Status: statusFailed, Error: err.Error(),
			}
			continue
		}
		if old, ok := byName[name]; ok && old.ParseStatus != "failed" && old.FileHash == hash {
			updates[i] = uploadRecord{
				Path: path, FileName: name, Hash: hash,
				Action: actionUnchanged, Status: statusUnchanged, KnowledgeID: old.ID,
			}
			continue
		}

		t := &uploadTask{path: path, name: name, hash: hash, action: actionCreate}
		if old, ok := byName[name]; ok {
			t.action = actionUpdate
			t.oldID = old.ID
		}
		taskByPath[path] = t
		idxByPath[path] = i
		uploadPaths = append(uploadPaths, path)
	}

	if len(uploadPaths) > 0 {
		results := runBatch(ctx, uploadPaths, concurrency, false,
			func(ctx context.Context, path string) (*client.Knowledge, error) {
				return uploadWithRetry(ctx, api, kbID, taskByPath[path], retry)
			}, progressFunc(cmd, len(uploadPaths)))
		if waitParse {
			applyParseWait(ctx, cmd, api, results, concurrency, pollInterval, parseTimeout)
		}
		for _, r := range results {
			t := taskByPath[r.Item]
			updates[idxByPath[r.Item]] = uploadRecord{
				Path: r.Item, FileName: t.name, Hash: t.hash,
				Action: t.action, Status: r.Status, KnowledgeID: r.KnowledgeID,
				DeletedOld: t.deleted, Attempts: t.attempts, Error: r.Error,
			}
		}
	}

	if len(deleteIDs) > 0 {
		results := runBatch(ctx, deleteIDs, concurrency, false,
			func(ctx context.Context, id string) (*client.Knowledge, error) {
				return nil, api.DeleteKnowledge(ctx, id)
			}, progressFunc(cmd, len(deleteIDs)))
		for _, r := range results {
			i := idxByID[r.Item]
			updates[i] = uploadRecord{
				Path: r.Item, FileName: state.Results[i].FileName,
				Action: actionDelete, Status: r.Status, Error: r.Error,
			}
		}
	}

	for i, rec := range updates {
		state.Results[i] = rec
	}
	state.FinishedAt = time.Now()
	if err := writeUploadState(statePath, *state); err != nil {
		return fmt.Errorf("保存重试结果失败: %w", err)
	}

	if isJSON(cmd) {
		return printJSON(cmd, state)
	}

	idxs := make([]int, 0, len(updates))
	for i := range updates {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)

	rows := make([][]string, 0, len(idxs))
	failed := 0
	for _, i := range idxs {
		r := state.Results[i]
		if r.Status == statusFailed {
			failed++
		}
		rows = append(rows, []string{
			defaultString(r.FileName, filepath.Base(r.Path)),
			r.Action,
			r.Status,
			defaultString(r.KnowledgeID, "-"),
			truncate(r.Error, 60),
		})
	}
	writeTable(cmd, []string{"FILE", "ACTION", "STATUS", "KNOWLEDGE_ID", "ERROR"}, rows)

	footerf(cmd, "重试 %d 项，仍失败 %d 项\n", len(idxs), failed)
	footerf(cmd, "结果已保存到 %s\n", statePath)
	if failed > 0 {
		return fmt.Errorf("%d 个文档重试后仍失败（结果见 %s）", failed, statePath)
	}
	return nil
}

// loadUploadState 读取并解析本地状态文件。
func loadUploadState(path string) (*uploadState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取状态文件失败: %w", err)
	}
	var state uploadState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("解析状态文件失败: %w", err)
	}
	return &state, nil
}

// locateLocalFile 返回记录对应的本地文件路径；原路径失效时尝试在 docDir 下按文件名查找。
// 找不到返回空字符串。
func locateLocalFile(recorded, docDir, name string) string {
	if recorded != "" {
		if info, err := os.Stat(recorded); err == nil && !info.IsDir() {
			return recorded
		}
	}
	if docDir != "" && name != "" {
		alt := filepath.Join(docDir, name)
		if info, err := os.Stat(alt); err == nil && !info.IsDir() {
			return alt
		}
	}
	return ""
}
