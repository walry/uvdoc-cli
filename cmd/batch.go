package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/client"
	"github.com/spf13/cobra"
)

// 批量操作的结果状态
const (
	statusOK        = "ok"
	statusDuplicate = "duplicate"
	statusFailed    = "failed"
	statusSkipped   = "skipped"
)

// batchResult 记录批量操作中单个条目的结果
type batchResult struct {
	Item        string `json:"item"`
	Status      string `json:"status"`
	KnowledgeID string `json:"knowledge_id,omitempty"`
	Error       string `json:"error,omitempty"`
}

// batchFunc 处理批量操作中的单个条目。返回的知识对象可以为 nil（例如删除操作）。
type batchFunc func(ctx context.Context, item string) (*client.Knowledge, error)

// runBatch 并发执行批量操作并按输入顺序返回结果。
// failFast 为 true 时，任一条目失败立即取消剩余任务（未执行的条目标记为 skipped）。
// onResult 在每个条目完成时调用，用于输出进度。
func runBatch(ctx context.Context, items []string, concurrency int, failFast bool,
	fn batchFunc, onResult func(int, batchResult),
) []batchResult {
	results := make([]batchResult, len(items))
	if len(items) == 0 {
		return results
	}
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > len(items) {
		concurrency = len(items)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	idxCh := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range idxCh {
				knowledge, err := fn(ctx, items[idx])
				r := batchResult{Item: items[idx], Status: statusOK}
				if knowledge != nil {
					r.KnowledgeID = knowledge.ID
				}
				switch {
				case err == nil:
				case errors.Is(err, client.ErrDuplicateFile), errors.Is(err, client.ErrDuplicateURL):
					// 服务端返回 409 时仍会带回已存在的知识条目
					r.Status = statusDuplicate
					r.Error = err.Error()
				default:
					r.Status = statusFailed
					r.Error = err.Error()
					if failFast {
						cancel()
					}
				}
				results[idx] = r
				if onResult != nil {
					onResult(idx, r)
				}
			}
		}()
	}

	// 单独投递任务，保证 worker 全部就绪后再开始分发
	go func() {
		defer close(idxCh)
		for i := range items {
			select {
			case idxCh <- i:
			case <-ctx.Done():
				return
			}
		}
	}()
	wg.Wait()

	for i, r := range results {
		if r.Status == "" {
			results[i] = batchResult{Item: items[i], Status: statusSkipped}
		}
	}
	return results
}

// countResults 统计各状态的条目数量
func countResults(results []batchResult) (ok, duplicate, failed int) {
	for _, r := range results {
		switch r.Status {
		case statusOK:
			ok++
		case statusDuplicate:
			duplicate++
		case statusFailed:
			failed++
		}
	}
	return ok, duplicate, failed
}

// renderBatchResults 输出批量结果
func renderBatchResults(cmd *cobra.Command, results []batchResult) error {
	if isJSON(cmd) {
		return printJSON(cmd, results)
	}
	rows := make([][]string, 0, len(results))
	for _, r := range results {
		rows = append(rows, []string{r.Item, r.Status, defaultString(r.KnowledgeID, "-"), r.Error})
	}
	writeTable(cmd, []string{"ITEM", "STATUS", "KNOWLEDGE_ID", "ERROR"}, rows)
	return nil
}

// batchError 把批量结果中的失败汇总为一个错误，便于设置退出码
func batchError(results []batchResult) error {
	ok, duplicate, failed := countResults(results)
	if failed > 0 {
		return fmt.Errorf("%d 个条目处理失败（成功 %d，重复 %d）", failed, ok, duplicate)
	}
	return nil
}

func defaultString(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// collectFiles 展开传入的路径：目录需配合 recursive 递归扫描，exts 用于扩展名过滤（为空表示不过滤）
func collectFiles(paths []string, recursive bool, exts []string) ([]string, error) {
	allowed := normalizeExts(exts)
	seen := make(map[string]struct{})
	var files []string

	add := func(path string) error {
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		if _, ok := seen[abs]; ok {
			return nil
		}
		seen[abs] = struct{}{}
		files = append(files, path)
		return nil
	}

	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			if !recursive {
				return nil, fmt.Errorf("%s 是目录，请添加 -r/--recursive 递归上传", p)
			}
			err := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				if matchExt(path, allowed) {
					return add(path)
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			continue
		}
		if matchExt(p, allowed) {
			if err := add(p); err != nil {
				return nil, err
			}
		}
	}

	sort.Strings(files)
	return files, nil
}

// normalizeExts 把 ["pdf", ".DOCX", "*.md"] 统一成 [".pdf", ".docx", ".md"]
func normalizeExts(exts []string) []string {
	normalized := make([]string, 0, len(exts))
	for _, ext := range exts {
		e := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ext), "*"))
		if e == "" {
			continue
		}
		if !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		normalized = append(normalized, strings.ToLower(e))
	}
	return normalized
}

func matchExt(path string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	ext := strings.ToLower(filepath.Ext(path))
	for _, a := range allowed {
		if ext == a {
			return true
		}
	}
	return false
}

// confirm 危险操作的二次确认
func confirm(cmd *cobra.Command, yes bool, message string) error {
	if yes {
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s [y/N]: ", message)
	var answer string
	if _, err := fmt.Fscanln(cmd.InOrStdin(), &answer); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	}
	return errors.New("操作已取消")
}

// pollLoop 定时调用 fn 轮询异步任务状态，fn 返回状态描述与是否已结束。
// 调用方通过 ctx 控制超时。返回最后一次状态描述。
func pollLoop(ctx context.Context, out io.Writer, interval time.Duration,
	fn func(context.Context) (string, bool, error),
) (string, error) {
	last := ""
	for {
		status, done, err := fn(ctx)
		if err != nil {
			return last, err
		}
		if status != "" {
			last = status
			if out != nil {
				fmt.Fprintln(out, status)
			}
		}
		if done {
			return last, nil
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(interval):
		}
	}
}
