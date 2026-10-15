package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileBinding(t *testing.T) {
	for _, tc := range []struct {
		name, role, private, public, inline string
		ok                                  bool
	}{
		{"gateway", "gateway", `{"app_id":"bot-123","app_secret":"test-secret"}`, "", "", true},
		{"worker", "worker", "", "bot-123\n", "", true},
		{"worker-rejects-secret-file", "worker", `{"app_id":"bot-123","app_secret":"test-secret"}`, "", "", false},
		{"gateway-rejects-public-file", "gateway", "", "bot-123", "", false},
		{"ambiguous-file", "gateway", `{"app_id":"bot-123","app_secret":"test-secret"}`, "bot-123", "", false},
		{"mixed-env", "gateway", `{"app_id":"bot-123","app_secret":"test-secret"}`, "", "other-bot", false},
		{"invalid-json", "gateway", `{"app_id":"bot-123","app_secret":"test-secret"`, "", "", false},
		{"empty-secret", "gateway", `{"app_id":"bot-123","app_secret":" "}`, "", "", false},
		{"invalid-id", "worker", "", "bot/123", "", false},
		{"oversized", "gateway", strings.Repeat("s", 8193), "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("QQ_APP_ID", tc.inline)
			t.Setenv("QQ_APP_SECRET", "")
			t.Setenv("QQ_CREDENTIALS_FILE", "")
			t.Setenv("QQ_APP_ID_FILE", "")
			t.Setenv("INTERNAL_API_TOKEN", strings.Repeat("x", 32))
			t.Setenv("MODEL_BASE_URL", "https://model.example/v1")
			t.Setenv("MODEL_API_KEY", "test-key")
			t.Setenv("MODEL_NAME", "test-model")
			dir := t.TempDir()
			for key, value := range map[string]string{"QQ_CREDENTIALS_FILE": tc.private, "QQ_APP_ID_FILE": tc.public} {
				if value == "" {
					continue
				}
				path := filepath.Join(dir, key)
				if err := os.WriteFile(path, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv(key, path)
			}
			c, err := Load(tc.role)
			if (err == nil) != tc.ok {
				t.Fatalf("ok=%v err=%v", tc.ok, err)
			}
			if err != nil && strings.Contains(err.Error(), "test-secret") {
				t.Fatal("credential leaked in error")
			}
			if tc.ok && c.AppID != "bot-123" {
				t.Fatal("wrong AppID")
			}
			if tc.ok && tc.role == "worker" && c.AppSecret != "" {
				t.Fatal("worker received secret")
			}
		})
	}
}

func TestMissingBindingFile(t *testing.T) {
	t.Setenv("QQ_APP_ID", "")
	t.Setenv("QQ_APP_SECRET", "")
	t.Setenv("QQ_APP_ID_FILE", "")
	t.Setenv("QQ_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "missing"))
	if _, err := Load("gateway"); err == nil || !strings.Contains(err.Error(), "QQ_CREDENTIALS_FILE") {
		t.Fatal(err)
	}
}
