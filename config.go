package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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

// AutoAssignConfig 自动指派: 当未指派任务的指定自定义字段从 FromValue 变为其他值时,
// 自动把指派者设置为当前 API 用户(自己)。
type AutoAssignConfig struct {
	Enabled     bool   `json:"enabled"`
	CustomField string `json:"custom_field"`
	FromValue   string `json:"from_value"`
}

// DailyReportConfig 每日报告: 每天到点把"当月已完成任务"统计发送到飞书。
// 过滤条件与统计页面一致(已关闭+当月+统计=1), 固定内置不可配置。
type DailyReportConfig struct {
	Enabled bool   `json:"enabled"`
	Time    string `json:"time"`    // 每天发送时间 "HH:MM"
	Project string `json:"project"` // 统计的项目标识, 如 eassist
	// WorkdaysOnly true 时只在法定工作日发送(跳过法定节假日/周末,
	// 调休补班日照常发送)。节假日数据按年从 timor.tech 拉取缓存,
	// 拉取失败降级为"周一~周五"判断。
	WorkdaysOnly bool `json:"workdays_only"`
}

type Config struct {
	RedmineURL   string `json:"redmine_url"`
	APIKey       string `json:"api_key"`
	PollInterval int    `json:"poll_interval"`
	LogFile      string `json:"log_file"`
	StateFile    string `json:"state_file"`
	Timezone     string `json:"timezone"`
	// InsecureSkipVerify 跳过 HTTPS 证书校验 (适用于自签名或证书过期的内网 Redmine)
	InsecureSkipVerify bool              `json:"insecure_skip_verify"`
	Feishu             FeishuConfig      `json:"feishu"`
	Filter             FilterConfig      `json:"filter"`
	AutoAssign         AutoAssignConfig  `json:"auto_assign"`
	DailyReport        DailyReportConfig `json:"daily_report"`
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
	if cfg.DailyReport.Enabled {
		if cfg.DailyReport.Project == "" {
			return cfg, fmt.Errorf("config.json 中 daily_report.project 不能为空")
		}
		if _, err := time.Parse("15:04", cfg.DailyReport.Time); err != nil {
			return cfg, fmt.Errorf("config.json 中 daily_report.time 格式应为 \"HH:MM\", 实际: %q", cfg.DailyReport.Time)
		}
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
