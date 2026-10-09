package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/client"
	"github.com/spf13/cobra"
)

// 服务端返回的文档解析状态（parse_status）。
const (
	parsePending    = "pending"
	parseProcessing = "processing"
	parseFinalizing = "finalizing"
	parseCompleted  = "completed"
	parseFailed     = "failed"
	parseCancelled  = "cancelled"
)

// parseTarget 是一个待等待解析完成的文档。
type parseTarget struct {
	item string // 原始条目（本地文件路径）
	id   string // 知识 ID
}

// parseOutcome 是单个文档解析等待的结果。
type parseOutcome struct {
	status string
	err    string
}

// applyParseWait 并发等待 results 中已成功上传的文档解析完成，并把解析结果写回 results：
//   - 解析完成（completed）保持 statusOK，即“上传成功且解析完成”；
//   - 解析失败 / 取消 / 超时改为 statusFailed，并带上失败原因。
//
// 控制台会以单行动画展示等待进度与预计剩余时间（JSON 模式下静默）。
func applyParseWait(ctx context.Context, cmd *cobra.Command, api *client.Client,
	results []batchResult, concurrency int, interval, timeout time.Duration,
) {
	targets := make([]parseTarget, 0, len(results))
	for _, r := range results {
		if r.Status == statusOK && r.KnowledgeID != "" {
			targets = append(targets, parseTarget{item: r.Item, id: r.KnowledgeID})
		}
	}
	if len(targets) == 0 {
		return
	}

	var out io.Writer
	if !isJSON(cmd) {
		out = cmd.ErrOrStderr()
	}

	outcomes := waitForParsing(ctx, out, api, targets, concurrency, interval, timeout)
	for i := range results {
		out, ok := outcomes[results[i].Item]
		if !ok {
			continue
		}
		results[i].Status = out.status
		if out.err != "" {
			results[i].Error = out.err
		}
	}
}

// waitForParsing 并发轮询每个文档的 parse_status 直到终态。
// out 非空时渲染实时等待动画；返回以条目为键的解析结果。
func waitForParsing(ctx context.Context, out io.Writer, api *client.Client,
	targets []parseTarget, concurrency int, interval, timeout time.Duration,
) map[string]parseOutcome {
	outcomes := make(map[string]parseOutcome, len(targets))
	if len(targets) == 0 {
		return outcomes
	}
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > len(targets) {
		concurrency = len(targets)
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}

	var mon *parseMonitor
	if out != nil {
		mon = newParseMonitor(out, len(targets))
		mon.start()
		defer mon.stop()
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	idxCh := make(chan int)
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range idxCh {
				t := targets[idx]
				begin := time.Now()
				if mon != nil {
					mon.begin()
				}
				status, errMsg := waitParseOne(ctx, api, t.id, interval, timeout)
				if mon != nil {
					mon.finish(time.Since(begin))
				}
				mu.Lock()
				outcomes[t.item] = parseOutcome{status: status, err: errMsg}
				mu.Unlock()
			}
		}()
	}

	go func() {
		defer close(idxCh)
		for i := range targets {
			select {
			case idxCh <- i:
			case <-ctx.Done():
				return
			}
		}
	}()
	wg.Wait()
	return outcomes
}

// waitParseOne 轮询单个文档，直到解析进入终态、超时或上下文取消。
// timeout <= 0 表示不超时，仅靠上下文取消退出。
func waitParseOne(ctx context.Context, api *client.Client, id string, interval, timeout time.Duration) (string, string) {
	hasDeadline := timeout > 0
	var deadline time.Time
	if hasDeadline {
		deadline = time.Now().Add(timeout)
	}
	for {
		k, err := api.GetKnowledge(ctx, id)
		if err != nil {
			return statusFailed, fmt.Sprintf("查询解析状态失败: %v", err)
		}
		switch k.ParseStatus {
		case parseCompleted:
			return statusOK, ""
		case parseFailed:
			return statusFailed, defaultString(k.ErrorMessage, "服务端解析失败")
		case parseCancelled:
			return statusFailed, "解析已取消"
		case parsePending, parseProcessing, parseFinalizing:
			// 仍在解析，继续等待。
		default:
			// 未知状态按未完成处理，避免误判为成功。
		}
		if hasDeadline && time.Now().After(deadline) {
			return statusFailed, fmt.Sprintf("解析超时 %s（当前状态 %s）",
				timeout, defaultString(k.ParseStatus, "unknown"))
		}
		select {
		case <-ctx.Done():
			return statusFailed, ctx.Err().Error()
		case <-time.After(interval):
		}
	}
}

// parseMonitor 在单行内渲染“等待解析”的实时动画：spinner + 进度 + 已用时间 + ETA。
type parseMonitor struct {
	out    io.Writer
	total  int
	frames []string

	mu      sync.Mutex
	done    int
	active  int
	sumDur  time.Duration
	lineLen int

	started time.Time
	stopCh  chan struct{}
	doneCh  chan struct{}
}

func newParseMonitor(out io.Writer, total int) *parseMonitor {
	return &parseMonitor{
		out:     out,
		total:   total,
		frames:  []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		started: time.Now(),
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
}

func (m *parseMonitor) start() {
	go func() {
		defer close(m.doneCh)
		ticker := time.NewTicker(120 * time.Millisecond)
		defer ticker.Stop()
		for frame := 0; ; frame++ {
			select {
			case <-m.stopCh:
				m.clear()
				return
			case <-ticker.C:
				m.render(frame)
			}
		}
	}()
}

// stop 终止动画，清空当前行并输出一条收尾摘要。
func (m *parseMonitor) stop() {
	close(m.stopCh)
	<-m.doneCh
	fmt.Fprintf(m.out, "文档解析完成，共 %d 篇，耗时 %s\n", m.total, formatDuration(time.Since(m.started)))
}

func (m *parseMonitor) begin() {
	m.mu.Lock()
	m.active++
	m.mu.Unlock()
}

func (m *parseMonitor) finish(d time.Duration) {
	m.mu.Lock()
	m.done++
	m.active--
	m.sumDur += d
	m.mu.Unlock()
}

func (m *parseMonitor) render(frame int) {
	m.mu.Lock()
	done, active, sumDur := m.done, m.active, m.sumDur
	m.mu.Unlock()

	spin := m.frames[frame%len(m.frames)]
	line := fmt.Sprintf("%s 等待解析 %d/%d（进行中 %d）已用 %s",
		spin, done, m.total, active, formatDuration(time.Since(m.started)))
	// 已完成的样本足够时按平均耗时估算 ETA。
	if done > 0 && done < m.total {
		per := sumDur / time.Duration(done)
		line += fmt.Sprintf("，预计剩余约 %s", formatDuration(per*time.Duration(m.total-done)))
	}
	m.print(line)
}

func (m *parseMonitor) print(line string) {
	pad := ""
	if w := displayWidth(line); w < m.lineLen {
		pad = strings.Repeat(" ", m.lineLen-w)
	} else if w > m.lineLen {
		m.lineLen = w
	}
	fmt.Fprintf(m.out, "\r%s%s", line, pad)
}

func (m *parseMonitor) clear() {
	fmt.Fprintf(m.out, "\r%s\r", strings.Repeat(" ", m.lineLen))
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return d.Round(100 * time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}
