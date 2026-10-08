package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/client"
)

// newMockAPI 启动一个 httptest 服务并返回指向它的 client，测试结束后自动关闭。
func newMockAPI(t *testing.T, handler http.HandlerFunc) *client.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return client.NewClient(srv.URL)
}

func respondJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func orgListResponse(names ...string) map[string]any {
	orgs := make([]map[string]any, 0, len(names))
	for _, name := range names {
		orgs = append(orgs, map[string]any{"id": "org-" + name, "name": name})
	}
	return map[string]any{"success": true, "data": map[string]any{"organizations": orgs}}
}

func TestShareNewKnowledgeBase_SharesToMatchingSpace(t *testing.T) {
	var shareBody map[string]string
	api := newMockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/organizations":
			respondJSON(w, orgListResponse("其它空间", uvDocSharedSpaceName))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge-bases/kb-1/shares":
			if err := json.NewDecoder(r.Body).Decode(&shareBody); err != nil {
				t.Errorf("decode share body: %v", err)
			}
			respondJSON(w, map[string]any{"success": true, "data": map[string]any{
				"id": "share-1", "knowledge_base_id": "kb-1",
				"organization_id": "org-" + uvDocSharedSpaceName, "permission": uvDocSharePermission,
			}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	if err := shareNewKnowledgeBase(context.Background(), api, "kb-1"); err != nil {
		t.Fatalf("shareNewKnowledgeBase returned error: %v", err)
	}
	if got := shareBody["organization_id"]; got != "org-"+uvDocSharedSpaceName {
		t.Errorf("organization_id = %q, want %q", got, "org-"+uvDocSharedSpaceName)
	}
	if got := shareBody["permission"]; got != uvDocSharePermission {
		t.Errorf("permission = %q, want %q", got, uvDocSharePermission)
	}
}

func TestShareNewKnowledgeBase_SpaceNotFound(t *testing.T) {
	api := newMockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, orgListResponse("其它空间"))
	})

	err := shareNewKnowledgeBase(context.Background(), api, "kb-1")
	if err == nil {
		t.Fatal("expected error when shared space is missing")
	}
	if !strings.Contains(err.Error(), uvDocSharedSpaceName) {
		t.Errorf("error %q should mention space name %q", err, uvDocSharedSpaceName)
	}
}

func TestShareNewKnowledgeBase_ShareRequestFails(t *testing.T) {
	api := newMockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			respondJSON(w, orgListResponse(uvDocSharedSpaceName))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	})

	if err := shareNewKnowledgeBase(context.Background(), api, "kb-1"); err == nil {
		t.Fatal("expected error when share request fails")
	}
}

// TestEnsureKnowledgeBaseThenShare 验证“新建知识库 -> 共享到 UVDoc 共享空间”这一完整流程：
// 知识库不存在时会被创建，随后用创建返回的 ID 共享到目标空间。
func TestEnsureKnowledgeBaseThenShare(t *testing.T) {
	var created, shared bool
	api := newMockAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/knowledge-bases":
			respondJSON(w, map[string]any{"success": true, "data": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/models":
			respondJSON(w, map[string]any{"success": true, "data": []any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge-bases":
			created = true
			respondJSON(w, map[string]any{"success": true, "data": map[string]any{
				"id": "kb-new", "name": "unipro_v1.0",
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/organizations":
			respondJSON(w, orgListResponse(uvDocSharedSpaceName))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge-bases/kb-new/shares":
			shared = true
			respondJSON(w, map[string]any{"success": true, "data": map[string]any{"id": "share-1"}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	ctx := context.Background()
	kb, isNew, err := ensureKnowledgeBase(ctx, api, "unipro_v1.0")
	if err != nil {
		t.Fatalf("ensureKnowledgeBase: %v", err)
	}
	if !isNew {
		t.Fatal("expected knowledge base to be reported as newly created")
	}
	if err := shareNewKnowledgeBase(ctx, api, kb.ID); err != nil {
		t.Fatalf("shareNewKnowledgeBase: %v", err)
	}
	if !created || !shared {
		t.Fatalf("created=%v shared=%v, want both true", created, shared)
	}
}
