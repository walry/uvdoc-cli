package cmd

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/client"
	"github.com/spf13/cobra"
)

// --doc-dir / --product / --release 支持从环境变量读取默认值
const (
	envDocDir  = "DOC_DIR"
	envProduct = "PRODUCT"
	envRelease = "RELEASE_NAME"
)

// 单个文档的处理动作
const (
	actionCreate    = "create"
	actionUpdate    = "update"
	actionUnchanged = "unchanged"
	actionDelete    = "delete"
	statusUnchanged = "unchanged"
)

// uploadOptions 是 upload 子命令解析后的参数
type uploadOptions struct {
	docDir       string
	product      string
	release      string
	deleteRemote bool
	retry        int
	concurrency  int
	recursive    bool
	exts         []string
	stateFile    string
}

// uploadTask 描述一个待上传文档及其处理方式
type uploadTask struct {
	path     string
	name     string
	hash     string
	action   string
	oldID    string
	deleted  bool
	attempts int
}

// uploadRecord 记录单个文档的处理结果，持久化到本地状态文件
type uploadRecord struct {
	Path        string `json:"path"`
	FileName    string `json:"file_name,omitempty"`
	Hash        string `json:"hash,omitempty"`
	Action      string `json:"action"`
	Status      string `json:"status"`
	KnowledgeID string `json:"knowledge_id,omitempty"`
	DeletedOld  bool   `json:"deleted_old,omitempty"`
	Attempts    int    `json:"attempts,omitempty"`
	Error       string `json:"error,omitempty"`
}

// uploadState 是一次 upload 运行的完整结果，保存在本地便于审计与重试
type uploadState struct {
	Product         string         `json:"product"`
	Release         string         `json:"release"`
	KnowledgeBase   string         `json:"knowledge_base"`
	KnowledgeBaseID string         `json:"knowledge_base_id"`
	DocDir          string         `json:"doc_dir"`
	StartedAt       time.Time      `json:"started_at"`
	FinishedAt      time.Time      `json:"finished_at"`
	Results         []uploadRecord `json:"results"`
}

// newPublishUploadCommand 构建顶层 upload 子命令：按 <产品>_<版本> 定位知识库并批量上传文档。
func newPublishUploadCommand() *cobra.Command {
	var o uploadOptions
	cmd := &cobra.Command{
		Use:   "upload",
		Short: "按 <产品>_<版本> 批量上传文档到对应知识库",
		Long: `upload 以 <product>_<release> 拼接知识库名称，自动创建缺失的知识库，
再批量上传 --doc-dir 下的文档，并把结果记录到本地状态文件。

每个文档会计算本地哈希（MD5）与知识库中同名文档比较：
  - 哈希一致：跳过，不重复上传；
  - 哈希不一致：先删除旧文档再上传新文档。

上传失败的文档会按 --retry 指定的次数自动重试；重跑命令时，
未变化的文档会被哈希跳过，因此可安全地用于继续重试失败项。
也可以使用子命令 upload retry 只重试状态文件中记录的失败项。`,
		Example: `  # 上传 ./docs 到知识库 unipro_v1.0
  uvdoc upload --doc-dir ./docs --product unipro --release v1.0

  # 参数全部来自环境变量，失败重试 3 次，并删除知识库中本地已不存在的文档
  DOC_DIR=./docs PRODUCT=unipro RELEASE_NAME=v1.0 \
    uvdoc upload --retry 3 --delete-remote`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateUploadOptions(o); err != nil {
				return err
			}
			return runUpload(cmd, o)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&o.docDir, "doc-dir", envOr(envDocDir, ""), "文档上传根目录（环境变量 DOC_DIR）")
	flags.StringVar(&o.product, "product", envOr(envProduct, ""), "产品名称（环境变量 PRODUCT）")
	flags.StringVar(&o.release, "release", envOr(envRelease, ""), "发布版本（环境变量 RELEASE_NAME）")
	flags.BoolVar(&o.deleteRemote, "delete-remote", false, "删除知识库中存在但本地目录已不存在的文档")
	flags.IntVar(&o.retry, "retry", 2, "失败文档的重试次数")
	flags.IntVarP(&o.concurrency, "concurrency", "j", 4, "并发上传数")
	flags.BoolVarP(&o.recursive, "recursive", "r", true, "递归扫描 --doc-dir 下的子目录")
	flags.StringSliceVar(&o.exts, "ext", nil, "按扩展名过滤，例如 pdf,docx（默认不过滤）")
	flags.StringVar(&o.stateFile, "state-file", "", "上传结果保存路径（默认 .uvdoc/upload-<知识库>.json）")

	cmd.AddCommand(newUploadRetryCommand())
	return cmd
}

func validateUploadOptions(o uploadOptions) error {
	if strings.TrimSpace(o.docDir) == "" {
		return fmt.Errorf("文档根目录为空，请使用 --doc-dir 或环境变量 %s 指定", envDocDir)
	}
	if strings.TrimSpace(o.product) == "" {
		return fmt.Errorf("产品名称为空，请使用 --product 或环境变量 %s 指定", envProduct)
	}
	if strings.TrimSpace(o.release) == "" {
		return fmt.Errorf("发布版本为空，请使用 --release 或环境变量 %s 指定", envRelease)
	}
	if o.retry < 0 {
		return fmt.Errorf("--retry 不能为负数")
	}
	if o.concurrency < 1 {
		return fmt.Errorf("--concurrency 必须大于 0")
	}
	info, err := os.Stat(o.docDir)
	if err != nil {
		return fmt.Errorf("无法访问 --doc-dir %s: %w", o.docDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("--doc-dir 必须是目录: %s", o.docDir)
	}
	return nil
}

func runUpload(cmd *cobra.Command, o uploadOptions) error {
	started := time.Now()
	ctx := cmd.Context()

	product := strings.TrimSpace(o.product)
	release := strings.TrimSpace(o.release)
	kbName := product + "_" + release

	statePath := o.stateFile
	if statePath == "" {
		statePath = filepath.Join(".uvdoc", "upload-"+sanitizeFileName(kbName)+".json")
	}
	stateAbs, err := filepath.Abs(statePath)
	if err != nil {
		return err
	}

	api := newClient(cmd)

	// 1. 校验知识库是否存在，不存在则创建。
	kb, created, err := ensureKnowledgeBase(ctx, api, kbName)
	if err != nil {
		return err
	}
	if created {
		footerf(cmd, "已创建知识库 %s (%s)\n", kb.Name, kb.ID)
	} else {
		footerf(cmd, "使用已存在知识库 %s (%s)\n", kb.Name, kb.ID)
	}

	// 2. 收集本地文档（排除状态文件本身）。
	files, err := collectFiles([]string{o.docDir}, o.recursive, o.exts)
	if err != nil {
		return err
	}
	files = excludePath(files, stateAbs)
	if len(files) == 0 {
		return fmt.Errorf("目录 %s 下没有匹配到待上传的文档", o.docDir)
	}

	// 3. 拉取知识库已有文档，按文件名建立索引。
	existing, err := listAllKnowledge(ctx, api, kb.ID)
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

	// 4. 通过哈希比较规划任务：未变化跳过，变化则先删后传。
	records := make(map[string]uploadRecord, len(files))
	taskByPath := make(map[string]*uploadTask)
	var toUpload []string
	for _, path := range files {
		name := filepath.Base(path)
		hash, err := fileMD5(path)
		if err != nil {
			records[path] = uploadRecord{
				Path: path, FileName: name, Action: actionCreate,
				Status: statusFailed, Error: err.Error(),
			}
			continue
		}
		if old, ok := byName[name]; ok && old.ParseStatus != "failed" && old.FileHash == hash {
			records[path] = uploadRecord{
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
		toUpload = append(toUpload, path)
	}

	// 5. 并发上传，复用 knowledge upload 的 CreateKnowledgeFromFile 逻辑。
	if len(toUpload) > 0 {
		results := runBatch(ctx, toUpload, o.concurrency, false,
			func(ctx context.Context, path string) (*client.Knowledge, error) {
				return uploadWithRetry(ctx, api, kb.ID, taskByPath[path], o.retry)
			}, progressFunc(cmd, len(toUpload)))
		for _, r := range results {
			t := taskByPath[r.Item]
			records[r.Item] = uploadRecord{
				Path: r.Item, FileName: t.name, Hash: t.hash,
				Action: t.action, Status: r.Status, KnowledgeID: r.KnowledgeID,
				DeletedOld: t.deleted, Attempts: t.attempts, Error: r.Error,
			}
		}
	}

	// 6. 可选：删除知识库中本地已不存在的文档。
	var pruned []uploadRecord
	if o.deleteRemote {
		pruned = pruneRemote(ctx, cmd, api, kb.ID, existing, files, o.concurrency)
	}

	// 7. 汇总并按本地文件顺序写入状态文件。
	all := make([]uploadRecord, 0, len(records)+len(pruned))
	for _, path := range files {
		if rec, ok := records[path]; ok {
			all = append(all, rec)
		}
	}
	all = append(all, pruned...)

	state := uploadState{
		Product: product, Release: release,
		KnowledgeBase: kb.Name, KnowledgeBaseID: kb.ID,
		DocDir: o.docDir, StartedAt: started, FinishedAt: time.Now(),
		Results: all,
	}
	if err := writeUploadState(statePath, state); err != nil {
		return fmt.Errorf("保存上传结果失败: %w", err)
	}

	if isJSON(cmd) {
		return printJSON(cmd, state)
	}

	rows := make([][]string, 0, len(all))
	for _, r := range all {
		rows = append(rows, []string{
			defaultString(r.FileName, filepath.Base(r.Path)),
			r.Action,
			r.Status,
			defaultString(r.KnowledgeID, "-"),
			truncate(r.Error, 60),
		})
	}
	writeTable(cmd, []string{"FILE", "ACTION", "STATUS", "KNOWLEDGE_ID", "ERROR"}, rows)

	uploaded, unchanged, duplicate, deleted, failed := countUploadRecords(all)
	footerf(cmd, "上传/更新成功 %d，未变化跳过 %d，重复跳过 %d，删除远端 %d，失败 %d\n",
		uploaded, unchanged, duplicate, deleted, failed)
	footerf(cmd, "结果已保存到 %s\n", statePath)
	if failed > 0 {
		return fmt.Errorf("%d 个文档处理失败（结果见 %s，可重新运行以重试）", failed, statePath)
	}
	return nil
}

// ensureKnowledgeBase 按名称查找知识库，不存在则创建。第二个返回值为是否新建。
func ensureKnowledgeBase(ctx context.Context, api *client.Client, name string) (*client.KnowledgeBase, bool, error) {
	bases, err := api.ListKnowledgeBases(ctx)
	if err != nil {
		return nil, false, err
	}
	for i := range bases {
		if bases[i].Name == name {
			return &bases[i], false, nil
		}
	}
	// 创建时未指定模型，用服务端模型列表中的默认项兜底（无匹配则为空）。
	embeddingModelID, chatModelID, err := defaultModelIDs(ctx, api)
	if err != nil {
		return nil, false, err
	}
	created, err := api.CreateKnowledgeBase(ctx, &client.KnowledgeBase{
		Name:             name,
		EmbeddingModelID: embeddingModelID,
		SummaryModelID:   chatModelID,
		ChunkingConfig: client.ChunkingConfig{
			ChunkSize:    512,
			ChunkOverlap: 50,
			Separators:   []string{"\n\n", "\n", ". ", "? ", "! "},
		},
	})
	if err != nil {
		return nil, false, err
	}
	return created, true, nil
}

// listAllKnowledge 自动翻页拉取知识库中的全部文档。
func listAllKnowledge(ctx context.Context, api *client.Client, kbID string) ([]client.Knowledge, error) {
	const pageSize = 100
	var all []client.Knowledge
	for page := 1; ; page++ {
		batch, total, err := api.ListKnowledgeWithFilter(ctx, kbID, page, pageSize, client.KnowledgeListFilter{})
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) == 0 || int64(len(all)) >= total {
			break
		}
	}
	return all, nil
}

// uploadWithRetry 上传单个文档，失败时按 retry 次数重试。
// 重复文件属于预期结果，不做重试。
func uploadWithRetry(ctx context.Context, api *client.Client, kbID string, t *uploadTask, retry int) (*client.Knowledge, error) {
	var lastErr error
	for attempt := 1; ; attempt++ {
		t.attempts = attempt
		k, err := uploadOne(ctx, api, kbID, t)
		if err == nil {
			return k, nil
		}
		if errors.Is(err, client.ErrDuplicateFile) || errors.Is(err, client.ErrDuplicateURL) {
			return k, err
		}
		lastErr = err
		if attempt > retry || ctx.Err() != nil {
			return nil, lastErr
		}
		select {
		case <-time.After(time.Duration(attempt) * time.Second):
		case <-ctx.Done():
			return nil, lastErr
		}
	}
}

// uploadOne 执行单次上传：更新场景先删除旧文档再上传，其余直接上传。
func uploadOne(ctx context.Context, api *client.Client, kbID string, t *uploadTask) (*client.Knowledge, error) {
	if t.action == actionUpdate && !t.deleted && t.oldID != "" {
		if err := api.DeleteKnowledge(ctx, t.oldID); err != nil {
			return nil, fmt.Errorf("删除旧文档失败: %w", err)
		}
		t.deleted = true
	}
	return api.CreateKnowledgeFromFile(ctx, kbID, t.path, nil, nil, "", "", nil)
}

// pruneRemote 删除知识库中文件名不在本地文档集合内的 file 类型文档。
func pruneRemote(ctx context.Context, cmd *cobra.Command, api *client.Client, kbID string,
	existing []client.Knowledge, files []string, concurrency int,
) []uploadRecord {
	local := make(map[string]bool, len(files))
	for _, f := range files {
		local[filepath.Base(f)] = true
	}

	var ids []string
	nameByID := make(map[string]string)
	for _, k := range existing {
		if k.Type != "file" || k.FileName == "" || local[k.FileName] {
			continue
		}
		ids = append(ids, k.ID)
		nameByID[k.ID] = k.FileName
	}
	if len(ids) == 0 {
		return nil
	}

	results := runBatch(ctx, ids, concurrency, false,
		func(ctx context.Context, id string) (*client.Knowledge, error) {
			return nil, api.DeleteKnowledge(ctx, id)
		}, progressFunc(cmd, len(ids)))

	out := make([]uploadRecord, 0, len(results))
	for _, r := range results {
		out = append(out, uploadRecord{
			Path: r.Item, FileName: nameByID[r.Item],
			Action: actionDelete, Status: r.Status, Error: r.Error,
		})
	}
	return out
}

// fileMD5 计算文件内容的 MD5，与服务端 file_hash 的算法保持一致。
func fileMD5(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// excludePath 从列表中剔除指定绝对路径（用于跳过状态文件）。
func excludePath(paths []string, abs string) []string {
	var out []string
	for _, p := range paths {
		if ap, err := filepath.Abs(p); err == nil && ap == abs {
			continue
		}
		out = append(out, p)
	}
	return out
}

func countUploadRecords(recs []uploadRecord) (uploaded, unchanged, duplicate, deleted, failed int) {
	for _, r := range recs {
		if r.Status == statusFailed {
			failed++
			continue
		}
		switch r.Action {
		case actionDelete:
			deleted++
		case actionUnchanged:
			unchanged++
		default:
			switch r.Status {
			case statusDuplicate:
				duplicate++
			case statusOK:
				uploaded++
			}
		}
	}
	return
}

func writeUploadState(path string, state uploadState) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

// sanitizeFileName 把知识库名转换成安全的文件名片段。
func sanitizeFileName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, s)
}
