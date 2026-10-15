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
