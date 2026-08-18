package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type NameObj struct {
	Name string `json:"name"`
}

type Detail struct {
	Property string      `json:"property"`
	Name     string      `json:"name"`
	OldValue interface{} `json:"old_value"`
	NewValue interface{} `json:"new_value"`
}

type Journal struct {
	ID        int64    `json:"id"`
	User      NameObj  `json:"user"`
	Notes     string   `json:"notes"`
	CreatedOn string   `json:"created_on"`
	Details   []Detail `json:"details"`
}

type Issue struct {
	ID             int64     `json:"id"`
	Subject        string    `json:"subject"`
	Project        NameObj   `json:"project"`
	Tracker        NameObj   `json:"tracker"`
	Status         NameObj   `json:"status"`
	Priority       NameObj   `json:"priority"`
	Author         NameObj   `json:"author"`
	AssignedTo     *NameObj  `json:"assigned_to"`
	Description    string    `json:"description"`
	StartDate      string    `json:"start_date"`
	DueDate        string    `json:"due_date"`
	EstimatedHours *float64  `json:"estimated_hours"`
	SpentHours     *float64  `json:"spent_hours"`
	DoneRatio      int       `json:"done_ratio"`
	CreatedOn      string    `json:"created_on"`
	UpdatedOn      string    `json:"updated_on"`
	Journals       []Journal `json:"journals"`
}

type issuesResponse struct {
	Issues     []Issue `json:"issues"`
	TotalCount int     `json:"total_count"`
}

type Watcher struct {
	cfg     Config
	baseDir string
	logger  *Logger
	state   *State
	loc     *time.Location
	client  *http.Client
	feishu  *FeishuClient
}

func newWatcher(cfg Config, baseDir string, logger *Logger) *Watcher {
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		logger.Printf("警告: 无法加载时区 %q, 回退到本地时区: %v", cfg.Timezone, err)
		loc = time.Local
	}
	w := &Watcher{
		cfg:     cfg,
		baseDir: baseDir,
		logger:  logger,
		state:   loadState(baseDir),
		loc:     loc,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
	if feishuEnabled(cfg) {
		w.feishu = newFeishuClient(cfg.Feishu)
		logger.Printf("飞书通知已启用: 接收人类型=%s", cfg.Feishu.ReceiveIDType)
	} else {
		logger.Printf("飞书通知未配置(跳过), 如需启用请填写 config.json 的 feishu 字段")
	}
	return w
}

func (w *Watcher) toLocal(iso string) string {
	if iso == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
	}
	return t.In(w.loc).Format("2006-01-02 15:04:05")
}

func (w *Watcher) redmineGet(path string, params url.Values) ([]byte, error) {
	u := strings.TrimRight(w.cfg.RedmineURL, "/") + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Redmine-API-Key", w.cfg.APIKey)
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Redmine 返回状态码 %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	return body, nil
}

func (w *Watcher) fetchUpdatedIssues(updatedAfter string) ([]Issue, error) {
	var issues []Issue
	for page := 1; ; page++ {
		params := url.Values{}
		params.Set("include", "journals")
		params.Set("sort", "updated_on:desc")
		params.Set("limit", "100")
		params.Set("page", fmt.Sprintf("%d", page))
		params.Set("updated_on", ">="+updatedAfter)

		body, err := w.redmineGet("/issues.json", params)
		if err != nil {
			return nil, err
		}
		var data issuesResponse
		if err := json.Unmarshal(body, &data); err != nil {
			return nil, fmt.Errorf("解析 issues.json 失败: %w", err)
		}
		issues = append(issues, data.Issues...)
		if page*100 >= data.TotalCount {
			break
		}
	}
	return issues, nil
}

func fmtVal(v interface{}) string {
	if v == nil {
		return "(空)"
	}
	s := fmt.Sprintf("%v", v)
	if s == "" {
		return "(空)"
	}
	return s
}

func (w *Watcher) renderIssue(issue Issue) string {
	var b strings.Builder
	assigned := ""
	if issue.AssignedTo != nil {
		assigned = issue.AssignedTo.Name
	}
	est := ""
	if issue.EstimatedHours != nil {
		est = fmt.Sprintf("%v", *issue.EstimatedHours)
	}
	spent := ""
	if issue.SpentHours != nil {
		spent = fmt.Sprintf("%v", *issue.SpentHours)
	}
	fmt.Fprintf(&b, "#%d 「%s」\n", issue.ID, fmtVal(issue.Subject))
	fmt.Fprintf(&b, "  项目: %s | 跟踪: %s | 状态: %s | 优先级: %s\n",
		issue.Project.Name, issue.Tracker.Name, issue.Status.Name, issue.Priority.Name)
	fmt.Fprintf(&b, "  指派给: %s | 作者: %s | 完成度: %d%%\n",
		assigned, issue.Author.Name, issue.DoneRatio)
	fmt.Fprintf(&b, "  开始: %s | 到期: %s | 预估工时: %sh | 已耗时: %sh\n",
		fmtVal(issue.StartDate), fmtVal(issue.DueDate), est, spent)
	fmt.Fprintf(&b, "  更新: %s", w.toLocal(issue.UpdatedOn))
	if len(issue.Journals) > 0 {
		last := issue.Journals[len(issue.Journals)-1]
		if last.User.Name != "" {
			fmt.Fprintf(&b, " (更新人: %s)", last.User.Name)
		}
	}
	fmt.Fprintln(&b)
	desc := strings.TrimSpace(issue.Description)
	if desc != "" {
		oneLine := strings.NewReplacer("\r\n", " ", "\n", " ").Replace(desc)
		if len(oneLine) > 200 {
			oneLine = oneLine[:200] + "..."
		}
		fmt.Fprintf(&b, "  描述: %s\n", oneLine)
	}
	return b.String()
}

func renderJournalDetails(j Journal) string {
	var b strings.Builder
	for _, d := range j.Details {
		switch d.Property {
		case "attr":
			fmt.Fprintf(&b, "    - 属性 [%s]: %s -> %s\n", d.Name, fmtVal(d.OldValue), fmtVal(d.NewValue))
		case "attachment":
			fmt.Fprintf(&b, "    - 附件 [%s] 添加\n", d.Name)
		default:
			fmt.Fprintf(&b, "    - 变更: %s -> %s\n", fmtVal(d.OldValue), fmtVal(d.NewValue))
		}
	}
	if notes := strings.TrimSpace(j.Notes); notes != "" {
		oneLine := strings.NewReplacer("\r\n", " ", "\n", " ").Replace(notes)
		fmt.Fprintf(&b, "    备注: %s\n", oneLine)
	}
	return b.String()
}

func (w *Watcher) printBlock(title, issueText, changeText, ts string) {
	sep := strings.Repeat("=", 72)
	first := strings.SplitN(issueText, "\n", 2)[0]
	w.logger.Printf(sep)
	w.logger.Printf("[%s] %s %s", title, first, ts)
	w.logger.Print(issueText)
	if changeText != "" {
		w.logger.Print("  变更明细:")
		w.logger.Print(changeText)
	}
	w.logger.Printf(sep)
}

func (w *Watcher) notify(title, color string, issue Issue, changeText string, j Journal, ts string) {
	if w.feishu == nil {
		return
	}
	var mdBody, note string
	if changeText != "" {
		mdBody = w.renderIssueCardMD(issue) + "\n\n" + changeText
	} else {
		mdBody = w.renderIssueCardMD(issue)
	}
	note = "时间: " + ts
	if j.User.Name != "" {
		note += " | 更新人: " + j.User.Name
	}
	issueURL := ""
	if w.cfg.RedmineURL != "" {
		issueURL = strings.TrimRight(w.cfg.RedmineURL, "/") + fmt.Sprintf("/issues/%d", issue.ID)
	}
	card := buildCard(title, mdBody, note, issueURL, color)
	if err := w.feishu.SendCard(card); err != nil {
		w.logger.Printf("飞书发送失败: %v", err)
	} else {
		w.logger.Printf("飞书通知已发送: %s", title)
	}
}

func (w *Watcher) processOnce() {
	nowUTC := time.Now().UTC().Format(time.RFC3339)
	lastUpdated := w.state.LastUpdated
	if lastUpdated == "" {
		lastUpdated = nowUTC
	}

	issues, err := w.fetchUpdatedIssues(lastUpdated)
	if err != nil {
		w.logger.Printf("错误: 请求 Redmine 失败: %v", err)
		return
	}

	for _, issue := range issues {
		key := fmt.Sprintf("%d", issue.ID)
		journals := issue.Journals
		sort.SliceStable(journals, func(i, j int) bool { return journals[i].ID < journals[j].ID })
		maxJID := int64(0)
		if len(journals) > 0 {
			maxJID = journals[len(journals)-1].ID
		}

		lastJID, known := w.state.SeenJournals[key]

		if !known {
			w.state.SeenJournals[key] = maxJID
			ts := w.toLocal(issue.CreatedOn)
			w.printBlock("新增任务", w.renderIssue(issue), "", ts)
			w.notify("新增任务", "green", issue, "", Journal{}, ts)
			continue
		}

		if maxJID <= lastJID {
			continue
		}

		for _, j := range journals {
			if j.ID <= lastJID {
				continue
			}
			if len(j.Details) == 0 && strings.TrimSpace(j.Notes) == "" {
				continue
			}
			ts := w.toLocal(j.CreatedOn)
			w.printBlock("属性变化", w.renderIssue(issue), renderJournalDetails(j), ts)
			w.notify("属性变化", "blue", issue, renderChangeCardMD(j), j, ts)
		}
		w.state.SeenJournals[key] = maxJID
	}

	w.state.LastUpdated = nowUTC
	if err := saveState(w.baseDir, w.state); err != nil {
		w.logger.Printf("警告: 保存状态失败: %v", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
