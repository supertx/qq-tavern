package config

import (
	"strings"
	"testing"
)

func TestStandaloneConfig(t *testing.T) {
	t.Setenv("QQ_APP_ID", "test-app")
	t.Setenv("QQ_APP_SECRET", "")
	t.Setenv("INTERNAL_API_TOKEN", strings.Repeat("x", 32))
	t.Setenv("MODEL_BASE_URL", "https://model.example/v1")
	t.Setenv("MODEL_API_KEY", "test-key")
	t.Setenv("MODEL_NAME", "test-model")
	c, err := Load("worker")
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode != "responses" || c.Effort != "medium" {
		t.Fatal(c)
	}
	t.Setenv("QQ_APP_SECRET", "must-not-reach-worker")
	if _, err := Load("worker"); err == nil {
		t.Fatal("accepted QQ secret in worker")
	}
}
func TestMissingConfig(t *testing.T) {
	t.Setenv("QQ_APP_ID", "")
	if _, err := Load("gateway"); err == nil || !strings.Contains(err.Error(), "QQ_APP_ID") {
		t.Fatal(err)
	}
}

func TestAdminTokenIsOptionalAndIndependent(t *testing.T) {
	t.Setenv("QQ_APP_ID", "test-app")
	t.Setenv("QQ_APP_SECRET", "")
	t.Setenv("INTERNAL_API_TOKEN", strings.Repeat("i", 32))
	t.Setenv("MODEL_BASE_URL", "https://model.example/v1")
	t.Setenv("MODEL_API_KEY", "test-key")
	t.Setenv("MODEL_NAME", "test-model")
	for _, token := range []string{"short", strings.Repeat("i", 32)} {
		t.Setenv("ADMIN_API_TOKEN", token)
		if _, err := Load("worker"); err == nil || !strings.Contains(err.Error(), "ADMIN_API_TOKEN") {
			t.Fatal(err)
		}
	}
	for _, token := range []string{"", strings.Repeat("a", 32)} {
		t.Setenv("ADMIN_API_TOKEN", token)
		c, err := Load("worker")
		if err != nil || c.AdminToken != token {
			t.Fatal(err)
		}
	}
}

func TestSummaryEffortSettings(t *testing.T) {
	t.Setenv("QQ_APP_ID", "test-app")
	t.Setenv("QQ_APP_SECRET", "")
	t.Setenv("INTERNAL_API_TOKEN", strings.Repeat("x", 32))
	t.Setenv("MODEL_BASE_URL", "https://open.bigmodel.cn/api/paas/v4")
	t.Setenv("MODEL_API_KEY", "test-key")
	t.Setenv("MODEL_NAME", "glm-5.3-flash")
	t.Setenv("MODEL_API_MODE", "chat_completions")
	t.Setenv("REASONING_EFFORT", "max")
	t.Setenv("SUMMARY_REASONING_EFFORT", "high")
	c, err := Load("worker")
	if err != nil || c.Effort != "max" || c.SummaryEffort != "high" {
		t.Fatal(c.Effort, c.SummaryEffort, err)
	}
	t.Setenv("SUMMARY_REASONING_EFFORT", "")
	c, err = Load("worker")
	if err != nil || c.SummaryEffort != "medium" {
		t.Fatal(c.SummaryEffort, err)
	}
	t.Setenv("SUMMARY_REASONING_EFFORT", "unknown")
	if _, err = Load("worker"); err == nil || !strings.Contains(err.Error(), "SUMMARY_REASONING_EFFORT") {
		t.Fatal(err)
	}
}
