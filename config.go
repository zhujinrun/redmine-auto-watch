package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type FeishuConfig struct {
	AppID         string `json:"app_id"`
	AppSecret     string `json:"app_secret"`
	APIBase       string `json:"api_base"`
	ReceiveIDType string `json:"receive_id_type"`
	ReceiveID     string `json:"receive_id"`
}

type Config struct {
	RedmineURL   string       `json:"redmine_url"`
	APIKey       string       `json:"api_key"`
	PollInterval int          `json:"poll_interval"`
	LogFile      string       `json:"log_file"`
	StateFile    string       `json:"state_file"`
	Timezone     string       `json:"timezone"`
	Feishu       FeishuConfig `json:"feishu"`
}

func defaultConfig() Config {
	return Config{
		RedmineURL:   "https://redmine.aiclick.net",
		PollInterval: 30,
		LogFile:      "watch.log",
		StateFile:    "state.json",
		Timezone:     "Asia/Shanghai",
	}
}

func loadConfig(baseDir string) (Config, error) {
	cfg := defaultConfig()
	path := filepath.Join(baseDir, "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("读取 config.json 失败: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("解析 config.json 失败: %w", err)
	}
	if cfg.RedmineURL == "" {
		return cfg, fmt.Errorf("config.json 中 redmine_url 不能为空")
	}
	if cfg.APIKey == "" || strings.Contains(cfg.APIKey, "在此填入") {
		return cfg, fmt.Errorf("config.json 中 api_key 未填写, 请在 Redmine 个人设置页获取 API Key")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 30
	}
	if cfg.Timezone == "" {
		cfg.Timezone = "Asia/Shanghai"
	}
	return cfg, nil
}

// feishuAppConfigured 判断 app_id/app_secret 是否已配置。
func feishuAppConfigured(cfg Config) bool {
	f := cfg.Feishu
	return f.AppID != "" && !strings.Contains(f.AppID, "在此填入") &&
		f.AppSecret != "" && !strings.Contains(f.AppSecret, "在此填入")
}

// feishuEnabled 判断飞书通知是否配置完整(含接收人)。
func feishuEnabled(cfg Config) bool {
	return feishuAppConfigured(cfg) &&
		cfg.Feishu.ReceiveID != "" && !strings.Contains(cfg.Feishu.ReceiveID, "在此填入")
}
