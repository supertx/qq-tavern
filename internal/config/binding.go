package config

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
)

var appIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Only gateway can read the private binding; worker receives an AppID-only file.
func loadQQBinding(role string, c *Config) error {
	private, public := os.Getenv("QQ_CREDENTIALS_FILE"), os.Getenv("QQ_APP_ID_FILE")
	if private == "" && public == "" {
		return nil
	}
	if c.AppID != "" || c.AppSecret != "" {
		return errors.New("QQ file binding cannot be combined with QQ_APP_ID or QQ_APP_SECRET")
	}
	if role == "gateway" {
		if private == "" || public != "" {
			return errors.New("gateway file binding requires only QQ_CREDENTIALS_FILE")
		}
		data, err := bindingFile(private, 8192)
		if err != nil {
			return errors.New("cannot read QQ_CREDENTIALS_FILE; run scripts/start.sh qr to create or repair binding")
		}
		var saved struct {
			AppID     string `json:"app_id"`
			AppSecret string `json:"app_secret"`
		}
		if json.Unmarshal(data, &saved) != nil || !appIDPattern.MatchString(saved.AppID) || strings.TrimSpace(saved.AppSecret) == "" || len(saved.AppSecret) > 4096 || strings.ContainsAny(saved.AppSecret, "\r\n\x00") {
			return errors.New("invalid QQ_CREDENTIALS_FILE; run scripts/start.sh qr --rebind")
		}
		c.AppID, c.AppSecret = saved.AppID, saved.AppSecret
	} else {
		if public == "" || private != "" {
			return errors.New("worker file binding requires only QQ_APP_ID_FILE; private credentials are gateway-only")
		}
		data, err := bindingFile(public, 128)
		if err != nil {
			return errors.New("cannot read QQ_APP_ID_FILE; run scripts/start.sh qr to create or repair binding")
		}
		c.AppID = strings.TrimSpace(string(data))
		if !appIDPattern.MatchString(c.AppID) {
			return errors.New("invalid QQ_APP_ID_FILE")
		}
	}
	return nil
}

func bindingFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("invalid binding file")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("binding file too large")
	}
	return data, nil
}
