package cmd

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// knowledgeResponse 构造 GET /api/v1/knowledge/{id} 的标准返回体。
func knowledgeResponse(parseStatus, errMsg string) map[string]any {
	return map[string]any{"success": true, "data": map[string]any{
		"id":            "k-1",
		"parse_status":  parseStatus,
		"error_message": errMsg,
	}}
}

// TestWaitParseOne_Timeout 验证 timeout > 0 时，文档一直处于 processing 会按超时报错。
func TestWaitParseOne_Timeout(t *testing.T) {
	api := newMockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, knowledgeResponse(parseProcessing, ""))
	})

	status, errMsg := waitParseOne(context.Background(), api, "k-1", 10*time.Millisecond, 80*time.Millisecond)
	if status != statusFailed {
		t.Fatalf("status = %q, want %q", status, statusFailed)
	}
	if !strings.Contains(errMsg, "解析超时") {
		t.Fatalf("errMsg = %q, want it to mention 解析超时", errMsg)
	}
}

// TestWaitParseOne_NoTimeout 验证 timeout <= 0 表示不超时：
// 即使文档长期 processing，也不会因解析超时退出，只会随上下文取消而结束。
func TestWaitParseOne_NoTimeout(t *testing.T) {
	var calls int64
	api := newMockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		respondJSON(w, knowledgeResponse(parseProcessing, ""))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	status, errMsg := waitParseOne(ctx, api, "k-1", 20*time.Millisecond, 0)
	if status != statusFailed {
		t.Fatalf("status = %q, want %q", status, statusFailed)
	}
	if strings.Contains(errMsg, "解析超时") {
		t.Fatalf("errMsg = %q, should not report 解析超时 when timeout<=0", errMsg)
	}
	if !strings.Contains(errMsg, context.DeadlineExceeded.Error()) {
		t.Fatalf("errMsg = %q, want it to reflect context cancellation", errMsg)
	}
	if n := atomic.LoadInt64(&calls); n < 2 {
		t.Fatalf("server called %d times, want it to keep polling before ctx cancel", n)
	}
}

// TestWaitParseOne_Completed 验证解析完成返回 statusOK。
func TestWaitParseOne_Completed(t *testing.T) {
	api := newMockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, knowledgeResponse(parseCompleted, ""))
	})

	status, errMsg := waitParseOne(context.Background(), api, "k-1", 10*time.Millisecond, 0)
	if status != statusOK || errMsg != "" {
		t.Fatalf("got (%q, %q), want (%q, \"\")", status, errMsg, statusOK)
	}
}

// TestWaitParseOne_Failed 验证解析失败时带上服务端错误信息。
func TestWaitParseOne_Failed(t *testing.T) {
	api := newMockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, knowledgeResponse(parseFailed, "boom"))
	})

	status, errMsg := waitParseOne(context.Background(), api, "k-1", 10*time.Millisecond, 0)
	if status != statusFailed || errMsg != "boom" {
		t.Fatalf("got (%q, %q), want (%q, %q)", status, errMsg, statusFailed, "boom")
	}
}

// TestParseTimeoutFlagDefault 验证 --parse-timeout 默认值为 0（不超时），且负数被拒绝。
func TestParseTimeoutFlagDefault(t *testing.T) {
	cmd := newPublishUploadCommand()
	def, err := cmd.Flags().GetDuration("parse-timeout")
	if err != nil {
		t.Fatalf("lookup parse-timeout flag: %v", err)
	}
	if def != 0 {
		t.Fatalf("default parse-timeout = %v, want 0 (never timeout)", def)
	}

	o := uploadOptions{
		docDir: t.TempDir(), product: "p", release: "r",
		retry: 0, concurrency: 1,
		waitParse: true, pollInterval: time.Second, parseTimeout: -time.Second,
	}
	if err := validateUploadOptions(o); err == nil {
		t.Fatal("expected error for negative --parse-timeout")
	}
}
