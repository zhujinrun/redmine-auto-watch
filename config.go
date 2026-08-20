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

// FilterConfig 监控过滤规则。
// 全部为空/未设置表示不限制。
type FilterConfig struct {
	// MonitorWeekdays 监控的工作日, 1=周一 ... 7=周日; 空 = 不限制
	MonitorWeekdays []int `json:"monitor_weekdays"`
	// MonitorStart/MonitorEnd 监控活跃时段, 24 小时制 "HH:MM" (含头不含尾); 空 = 不限制
	MonitorStart string `json:"monitor_start"`
	MonitorEnd   string `json:"monitor_end"`
	// Projects 只监控指定项目 (匹配任务的项目名, 忽略大小写, 支持子串); 空 = 全部
	Projects []string `json:"projects"`
	// Assignees 只监控指派给这些人的任务; 空 = 全部
	Assignees []string `json:"assignees"`
	// Attributes 只报告这些属性的变化 (journal detail.name), 如 status/priority/assigned_to; 空 = 全部
	Attributes []string `json:"attributes"`
	// CheckboxFields 自定义字段中按"是否"显示的字段名 (值为 1 显示"是", 0 显示"否")
	CheckboxFields []string `json:"checkbox_fields"`
}

type Config struct {
	RedmineURL   string       `json:"redmine_url"`
	APIKey       string       `json:"api_key"`
	PollInterval int          `json:"poll_interval"`
	LogFile      string       `json:"log_file"`
	StateFile    string       `json:"state_file"`
	Timezone     string       `json:"timezone"`
	Feishu       FeishuConfig `json:"feishu"`
	Filter       FilterConfig `json:"filter"`
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
