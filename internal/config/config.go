package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppID, AppSecret, RedisURL, Listen, InternalToken, Database, GatewayURL                     string
	QQAPIBase, QQTokenURL                                                                       string
	Observe                                                                                     bool
	ModelBase, ModelKey, Model, Mode, Effort, SummaryModel                                      string
	ImageBase, ImageKey, ImageModel, SpeechBase, SpeechKey, SpeechModel, SpeechVoice, SearchURL string
	MaxConcurrency                                                                              int
}

func Load(role string) (Config, error) {
	c := Config{AppID: os.Getenv("QQ_APP_ID"), AppSecret: os.Getenv("QQ_APP_SECRET"), RedisURL: env("REDIS_URL", "redis://redis:6379/0"), Listen: env("LISTEN_ADDR", ":8080"), InternalToken: os.Getenv("INTERNAL_API_TOKEN"), Database: env("DATABASE_PATH", "/data/"+role+".db"), GatewayURL: env("GATEWAY_URL", "http://gateway:8080"), QQAPIBase: env("QQ_API_BASE", "https://api.bot.qq.com"), QQTokenURL: env("QQ_TOKEN_URL", "https://api.bot.qq.com/app/getAppAccessToken"), Observe: os.Getenv("QQ_OBSERVE_GROUP_MESSAGES") == "true", ModelBase: os.Getenv("MODEL_BASE_URL"), ModelKey: os.Getenv("MODEL_API_KEY"), Model: os.Getenv("MODEL_NAME"), Mode: env("MODEL_API_MODE", "responses"), Effort: env("REASONING_EFFORT", "medium"), SummaryModel: os.Getenv("SUMMARY_MODEL"), ImageBase: os.Getenv("IMAGE_BASE_URL"), ImageKey: os.Getenv("IMAGE_API_KEY"), ImageModel: os.Getenv("IMAGE_MODEL"), SpeechBase: os.Getenv("SPEECH_BASE_URL"), SpeechKey: os.Getenv("SPEECH_API_KEY"), SpeechModel: os.Getenv("SPEECH_MODEL"), SpeechVoice: env("SPEECH_VOICE", "alloy"), SearchURL: os.Getenv("SEARXNG_URL"), MaxConcurrency: 4}
	if v := os.Getenv("MAX_CONCURRENCY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 16 {
			return c, errors.New("MAX_CONCURRENCY must be between 1 and 16")
		}
		c.MaxConcurrency = n
	}
	if role != "gateway" && role != "worker" {
		return c, errors.New("role must be gateway or worker")
	}
	if err := loadQQBinding(role, &c); err != nil {
		return c, err
	}
	if strings.TrimSpace(c.AppID) == "" {
		return c, errors.New("QQ_APP_ID is required")
	}
	if len(c.InternalToken) < 32 {
		return c, errors.New("INTERNAL_API_TOKEN must contain at least 32 characters")
	}
	if c.RedisURL == "" {
		return c, errors.New("REDIS_URL is required")
	}
	if role == "gateway" {
		if c.AppSecret == "" {
			return c, errors.New("QQ_APP_SECRET is required for gateway")
		}
		return c, nil
	}
	if c.AppSecret != "" {
		return c, errors.New("QQ_APP_SECRET must only be supplied to gateway")
	}
	if c.ModelBase == "" || c.ModelKey == "" || c.Model == "" {
		return c, errors.New("MODEL_BASE_URL, MODEL_API_KEY and MODEL_NAME are required for worker")
	}
	if c.SummaryModel == "" {
		c.SummaryModel = c.Model
	}
	if c.Mode != "responses" && c.Mode != "chat_completions" {
		return c, errors.New("MODEL_API_MODE must be responses or chat_completions")
	}
	switch c.Effort {
	case "none", "minimal", "low", "medium", "high", "xhigh":
	default:
		return c, errors.New("invalid REASONING_EFFORT")
	}
	for name, raw := range map[string]string{"GATEWAY_URL": c.GatewayURL, "MODEL_BASE_URL": c.ModelBase, "IMAGE_BASE_URL": c.ImageBase, "SPEECH_BASE_URL": c.SpeechBase, "SEARXNG_URL": c.SearchURL} {
		if raw != "" {
			u, e := url.Parse(raw)
			if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return c, fmt.Errorf("%s must be an HTTP(S) base URL without credentials/query/fragment", name)
			}
		}
	}
	if (c.ImageBase != "" || c.ImageKey != "" || c.ImageModel != "") && (c.ImageBase == "" || c.ImageKey == "" || c.ImageModel == "") {
		return c, errors.New("IMAGE_BASE_URL, IMAGE_API_KEY and IMAGE_MODEL must be configured together")
	}
	if (c.SpeechBase != "" || c.SpeechKey != "" || c.SpeechModel != "") && (c.SpeechBase == "" || c.SpeechKey == "" || c.SpeechModel == "") {
		return c, errors.New("SPEECH_BASE_URL, SPEECH_API_KEY and SPEECH_MODEL must be configured together")
	}
	return c, nil
}
func env(k, d string) string {
	if s := os.Getenv(k); s != "" {
		return s
	}
	return d
}

const TurnBudget = 285 * time.Second
