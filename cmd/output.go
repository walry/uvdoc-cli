package cmd

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// isJSON 判断当前是否要求 JSON 输出
func isJSON(cmd *cobra.Command) bool { return opts.output == "json" }

// printJSON 以缩进格式输出 JSON
func printJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// writeTable 输出对齐的表格。
// 这里不用 tabwriter：它按 rune 数计算列宽，中日韩全角字符会占两列导致错位，
// 因此按终端显示宽度自行补齐空格。
func writeTable(cmd *cobra.Command, headers []string, rows [][]string) {
	out := cmd.OutOrStdout()
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = displayWidth(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) {
				if w := displayWidth(cell); w > widths[i] {
					widths[i] = w
				}
			}
		}
	}

	printRow := func(cells []string) {
		var sb strings.Builder
		for i, cell := range cells {
			if i > 0 {
				sb.WriteString("  ")
			}
			sb.WriteString(cell)
			if i < len(widths) {
				sb.WriteString(strings.Repeat(" ", widths[i]-displayWidth(cell)))
			}
		}
		fmt.Fprintln(out, strings.TrimRight(sb.String(), " "))
	}

	printRow(headers)
	for _, row := range rows {
		printRow(row)
	}
}

// displayWidth 计算字符串的终端显示宽度，全角字符按 2 列计算
func displayWidth(s string) int {
	width := 0
	for _, r := range s {
		switch {
		case r < 0x7f:
			width++
		case isWideRune(r):
			width += 2
		default:
			width++
		}
	}
	return width
}

// isWideRune 判断是否为东亚宽字符（CJK、全角标点、假名、谚文等）
func isWideRune(r rune) bool {
	return (r >= 0x1100 && r <= 0x115f) ||
		(r >= 0x2e80 && r <= 0xa4cf) ||
		(r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe30 && r <= 0xfe6f) ||
		(r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) ||
		(r >= 0x20000 && r <= 0x3fffd)
}

// renderList 输出列表：JSON 模式直接序列化，表格模式逐行渲染
func renderList[T any](cmd *cobra.Command, items []T, headers []string, row func(T) []string) error {
	if isJSON(cmd) {
		return printJSON(cmd, items)
	}
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		rows = append(rows, row(item))
	}
	writeTable(cmd, headers, rows)
	return nil
}

// renderOne 输出单个对象
func renderOne[T any](cmd *cobra.Command, item T, headers []string, row func(T) []string) error {
	if isJSON(cmd) {
		return printJSON(cmd, item)
	}
	writeTable(cmd, headers, [][]string{row(item)})
	return nil
}

// renderPairs 输出键值对详情，保持给定的顺序
func renderPairs(cmd *cobra.Command, pairs [][2]string) error {
	if isJSON(cmd) {
		m := make(map[string]string, len(pairs))
		for _, p := range pairs {
			m[p[0]] = p[1]
		}
		return printJSON(cmd, m)
	}
	rows := make([][]string, 0, len(pairs))
	for _, p := range pairs {
		rows = append(rows, []string{p[0], p[1]})
	}
	writeTable(cmd, []string{"FIELD", "VALUE"}, rows)
	return nil
}

// renderMessage 输出操作结果提示，JSON 模式输出为 JSON 对象
func renderMessage(cmd *cobra.Command, message string, pairs [][2]string) error {
	if isJSON(cmd) {
		m := map[string]string{"message": message}
		for _, p := range pairs {
			m[p[0]] = p[1]
		}
		return printJSON(cmd, m)
	}
	fmt.Fprintln(cmd.OutOrStdout(), message)
	for _, p := range pairs {
		if p[1] == "" {
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s\n", p[0], p[1])
	}
	return nil
}

// footerf 仅在表格模式下输出尾部统计信息，避免污染 JSON 输出
func footerf(cmd *cobra.Command, format string, args ...any) {
	if isJSON(cmd) {
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), format, args...)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func formatBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func formatInt64(v int64) string {
	return strconv.FormatInt(v, 10)
}

func formatSize(size int64) string {
	if size <= 0 {
		return "-"
	}
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%dB", size)
	}
	value := float64(size)
	units := []string{"K", "M", "G", "T"}
	for _, u := range units {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f%s", value, u)
		}
	}
	return fmt.Sprintf("%.1fP", value/unit)
}

func truncate(s string, max int) string {
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max-1]) + "…"
}

// parseTime 兼容 RFC3339、日期时间、纯日期三种输入
func parseTime(s string) (time.Time, error) {
	layouts := []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"}
	var lastErr error
	for _, layout := range layouts {
		t, err := time.ParseInLocation(layout, s, time.Local)
		if err == nil {
			return t, nil
		}
		lastErr = err
	}
	return time.Time{}, fmt.Errorf("无法解析时间 %q（支持 RFC3339 / 2006-01-02 15:04:05 / 2006-01-02）: %w", s, lastErr)
}
