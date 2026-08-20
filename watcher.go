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

type CustomField struct {
	ID    int         `json:"id"`
	Name  string      `json:"name"`
	Value interface{} `json:"value"`
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
	DoneRatio      int           `json:"done_ratio"`
	CreatedOn      string        `json:"created_on"`
	UpdatedOn      string        `json:"updated_on"`
	CustomFields   []CustomField `json:"custom_fields"`
	Journals       []Journal     `json:"journals"`
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

	projectSet      map[string]bool
	assigneeSet     map[string]bool
	matchUnassigned bool
	attrSet         map[string]bool
	checkboxSet     map[string]bool
	weekdaySet      map[int]bool
	startMin        int
	endMin          int
	windowSet       bool
	wasOffWindow    bool
	selfUserID      int64
}

func newWatcher(cfg Config, baseDir string, logger *Logger) *Watcher {
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		logger.Printf("警告: 无法加载时区 %q, 回退到本地时区: %v", cfg.Timezone, err)
		loc = time.Local
	}
	w := &Watcher{
		cfg:         cfg,
		baseDir:     baseDir,
		logger:      logger,
		state:       loadState(baseDir),
		loc:         loc,
		client:      &http.Client{Timeout: 30 * time.Second},
projectSet:      toLowerSet(cfg.Filter.Projects),
		assigneeSet:     toLowerSet(cfg.Filter.Assignees),
		attrSet:         expandAttrNames(cfg.Filter.Attributes),
		checkboxSet:     toSet(cfg.Filter.CheckboxFields),
		weekdaySet:      make(map[int]bool),
		startMin:     -1,
		endMin:       -1,
	}
	// assignees 中的空字符串或关键字 "unassigned" 表示匹配"未指派"的任务
	for _, it := range cfg.Filter.Assignees {
		if strings.TrimSpace(it) == "" || strings.EqualFold(strings.TrimSpace(it), "unassigned") {
			w.matchUnassigned = true
			delete(w.assigneeSet, strings.ToLower(strings.TrimSpace(it)))
		}
	}
	for _, d := range cfg.Filter.MonitorWeekdays {
		if d >= 1 && d <= 7 {
			w.weekdaySet[d] = true
		}
	}
	if cfg.Filter.MonitorStart != "" {
		t, err := time.Parse("15:04", cfg.Filter.MonitorStart)
		if err == nil {
			w.startMin = t.Hour()*60 + t.Minute()
		} else {
			logger.Printf("警告: 无法解析 monitor_start %q, 忽略时段限制", cfg.Filter.MonitorStart)
		}
	}
	if cfg.Filter.MonitorEnd != "" {
		t, err := time.Parse("15:04", cfg.Filter.MonitorEnd)
		if err == nil {
			w.endMin = t.Hour()*60 + t.Minute()
		} else {
			logger.Printf("警告: 无法解析 monitor_end %q, 忽略时段限制", cfg.Filter.MonitorEnd)
		}
	}
	w.windowSet = len(w.weekdaySet) > 0 || w.startMin >= 0 || w.endMin >= 0
	if w.windowSet {
		logger.Printf("监控时段已启用: %s", w.windowDesc())
	}
	if len(w.projectSet) > 0 {
		logger.Printf("项目过滤: %v", cfg.Filter.Projects)
	}
	if len(w.assigneeSet) > 0 {
		logger.Printf("指派给过滤: %v", cfg.Filter.Assignees)
	}
	if len(w.attrSet) > 0 {
		logger.Printf("属性过滤: %v", cfg.Filter.Attributes)
	}
	if cfg.AutoAssign.Enabled {
		if id, err := w.fetchCurrentUserID(); err != nil {
			logger.Printf("警告: 自动指派已启用但获取当前用户失败: %v (自动指派不可用)", err)
		} else {
			w.selfUserID = id
			logger.Printf("自动指派已启用: 未指派任务的自定义字段 [%s] 从 %q 变为其他值时, 指派给当前用户(id=%d)",
				cfg.AutoAssign.CustomField, cfg.AutoAssign.FromValue, id)
		}
	}
	if feishuEnabled(cfg) {
		w.feishu = newFeishuClient(cfg.Feishu)
		logger.Printf("飞书通知已启用: 接收人类型=%s", cfg.Feishu.ReceiveIDType)
	} else {
		logger.Printf("飞书通知未配置(跳过), 如需启用请填写 config.json 的 feishu 字段")
	}
	return w
}

func toSet(items []string) map[string]bool {
	m := make(map[string]bool)
	for _, it := range items {
		if it != "" {
			m[it] = true
		}
	}
	return m
}

func toLowerSet(items []string) map[string]bool {
	m := make(map[string]bool)
	for _, it := range items {
		if it != "" {
			m[strings.ToLower(strings.TrimSpace(it))] = true
		}
	}
	return m
}

// attrAliases 新旧 Redmine 的 journal detail 名称对照。
// 新版用 "assigned_to_id"(值为对象ID), 旧版用 "assigned_to"(值为对象名)。
var attrAliases = map[string][]string{
	"assigned_to":   {"assigned_to_id"},
	"assigned_to_id": {"assigned_to"},
	"priority":      {"priority_id"},
	"priority_id":   {"priority"},
	"status":        {"status_id"},
	"status_id":     {"status"},
	"category":      {"category_id"},
	"category_id":   {"category"},
	"fixed_version": {"fixed_version_id"},
	"fixed_version_id": {"fixed_version"},
	"project":       {"project_id"},
	"project_id":    {"project"},
	"tracker":       {"tracker_id"},
	"tracker_id":    {"tracker"},
	"parent":        {"parent_id"},
	"parent_id":     {"parent"},
	"author":        {"author_id"},
	"author_id":     {"author"},
}

// expandAttrNames 生成属性过滤集合, 将配置项与其新旧名称别名一并加入。
func expandAttrNames(items []string) map[string]bool {
	m := make(map[string]bool)
	for _, it := range items {
		if it == "" {
			continue
		}
		m[it] = true
		for _, a := range attrAliases[it] {
			m[a] = true
		}
	}
	return m
}

// cfNameMap 由任务的 custom_fields 构建 字段ID -> 显示名称 映射。
func cfNameMap(issue Issue) map[string]string {
	m := make(map[string]string)
	for _, cf := range issue.CustomFields {
		m[fmt.Sprintf("%d", cf.ID)] = cf.Name
	}
	return m
}

func (w *Watcher) windowDesc() string {
	var parts []string
	if len(w.weekdaySet) > 0 {
		days := []string{"周一", "周二", "周三", "周四", "周五", "周六", "周日"}
		var ds []string
		for d := 1; d <= 7; d++ {
			if w.weekdaySet[d] {
				ds = append(ds, days[d-1])
			}
		}
		parts = append(parts, strings.Join(ds, "/"))
	}
	if w.startMin >= 0 && w.endMin >= 0 {
		parts = append(parts, fmt.Sprintf("%02d:%02d-%02d:%02d",
			w.startMin/60, w.startMin%60, w.endMin/60, w.endMin%60))
	}
	if len(parts) == 0 {
		return "全天"
	}
	return strings.Join(parts, " ")
}

func (w *Watcher) inWindow(now time.Time) bool {
	if !w.windowSet {
		return true
	}
	wd := int(now.Weekday())
	if wd == 0 {
		wd = 7
	}
	if len(w.weekdaySet) > 0 && !w.weekdaySet[wd] {
		return false
	}
	if w.startMin < 0 || w.endMin < 0 {
		return true
	}
	cur := now.Hour()*60 + now.Minute()
	if w.startMin <= w.endMin {
		return cur >= w.startMin && cur < w.endMin
	}
	// 跨天时段, 如 22:00 - 06:00
	return cur >= w.startMin || cur < w.endMin
}

// matchName 判断实际名称是否命中配置项 (忽略大小写, 支持子串匹配)。
func matchName(set map[string]bool, name string) bool {
	if len(set) == 0 {
		return true
	}
	lower := strings.ToLower(name)
	for k := range set {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

func (w *Watcher) issueMatches(issue Issue) bool {
	if !matchName(w.projectSet, issue.Project.Name) {
		return false
	}
	if w.matchUnassigned || len(w.assigneeSet) > 0 {
		assigned := ""
		if issue.AssignedTo != nil {
			assigned = issue.AssignedTo.Name
		}
		if assigned == "" {
			if !w.matchUnassigned {
				return false
			}
		} else if len(w.assigneeSet) == 0 || !matchName(w.assigneeSet, assigned) {
			return false
		}
	}
	return true
}

// journalRelevant 判断该 journal 是否需要报告。
// 新版 Redmine 的引用类属性 name 带 _id 后缀(如 assigned_to_id), 自定义字段 name 为字段ID;
// 通过别名展开和 cf 名称映射, 用户只需写显示名(如 assigned_to/权重)即可命中。
func (w *Watcher) journalRelevant(issue Issue, j Journal) bool {
	if len(w.attrSet) == 0 {
		return len(j.Details) > 0 || strings.TrimSpace(j.Notes) != ""
	}
	cfNames := cfNameMap(issue)
	for _, d := range j.Details {
		name := d.Name
		if d.Property == "cf" {
			if n, ok := cfNames[d.Name]; ok {
				name = n
			}
		}
		if w.attrSet[name] {
			return true
		}
		if d.Property == "attachment" && w.attrSet["attachment"] {
			return true
		}
	}
	return false
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

// fetchIssueJournals 单独拉取单个任务的详情与日志。
// 注: 部分 Redmine 实例的列表接口即使传 include=journals 也不返回 journals,
// 因此必须对每个更新的任务单独请求 /issues/{id}.json。
func (w *Watcher) fetchIssueJournals(id int64) (Issue, error) {
	params := url.Values{}
	params.Set("include", "journals")
	body, err := w.redmineGet(fmt.Sprintf("/issues/%d.json", id), params)
	if err != nil {
		return Issue{}, err
	}
	var data struct {
		Issue Issue `json:"issue"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return Issue{}, fmt.Errorf("解析 issues/%d.json 失败: %w", id, err)
	}
	return data.Issue, nil
}

// fetchCurrentUserID 获取当前 API 用户(自己)的用户 ID。
func (w *Watcher) fetchCurrentUserID() (int64, error) {
	body, err := w.redmineGet("/users/current.json", nil)
	if err != nil {
		return 0, err
	}
	var data struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, fmt.Errorf("解析 users/current.json 失败: %w", err)
	}
	return data.User.ID, nil
}

// shouldAutoAssign 判断该 journal 是否满足自动指派条件:
// 任务当前未指派, 且指定自定义字段从 FromValue 变为其他非空值。
func (w *Watcher) shouldAutoAssign(issue Issue, j Journal) bool {
	if w.selfUserID == 0 || issue.AssignedTo != nil {
		return false
	}
	cf := w.cfg.AutoAssign.CustomField
	if cf == "" {
		return false
	}
	cfNames := cfNameMap(issue)
	for _, d := range j.Details {
		if d.Property != "cf" {
			continue
		}
		name := d.Name
		if n, ok := cfNames[d.Name]; ok {
			name = n
		}
		if name != cf {
			continue
		}
		old, new := fmtVal(d.OldValue), fmtVal(d.NewValue)
		if old == w.cfg.AutoAssign.FromValue && new != old && new != "(空)" {
			return true
		}
	}
	return false
}

// autoAssign 把任务指派给当前 API 用户。
func (w *Watcher) autoAssign(id int64) error {
	payload := fmt.Sprintf(`{"issue":{"assigned_to_id":%d}}`, w.selfUserID)
	u := strings.TrimRight(w.cfg.RedmineURL, "/") + fmt.Sprintf("/issues/%d.json", id)
	req, err := http.NewRequest(http.MethodPut, u, strings.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Redmine-API-Key", w.cfg.APIKey)
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Redmine 返回状态码 %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	return nil
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

// renderCustomFields 返回 "权重: 10 | 统计: 是" 形式的自定义字段串, 无字段时返回空串。
// checkboxSet 中的字段按"是否"显示 (1=是, 0=否)。
func (w *Watcher) renderCustomFields(issue Issue) string {
	if len(issue.CustomFields) == 0 {
		return ""
	}
	parts := make([]string, 0, len(issue.CustomFields))
	for _, cf := range issue.CustomFields {
		v := fmtVal(cf.Value)
		if v == "(空)" {
			v = "-"
		} else if w.checkboxSet[cf.Name] {
			switch v {
			case "1":
				v = "是"
			case "0":
				v = "否"
			}
		}
		parts = append(parts, fmt.Sprintf("%s: %s", cf.Name, v))
	}
	return strings.Join(parts, " | ")
}

func (w *Watcher) renderIssue(issue Issue) string {
	var b strings.Builder
	assigned := ""
	if issue.AssignedTo != nil {
		assigned = issue.AssignedTo.Name
	}
	fmt.Fprintf(&b, "#%d 「%s」\n", issue.ID, fmtVal(issue.Subject))
	fmt.Fprintf(&b, "  项目: %s | 跟踪: %s | 状态: %s | 优先级: %s\n",
		issue.Project.Name, issue.Tracker.Name, issue.Status.Name, issue.Priority.Name)
	fmt.Fprintf(&b, "  指派给: %s | 作者: %s | 开始: %s\n",
		assigned, issue.Author.Name, fmtVal(issue.StartDate))
	if cf := w.renderCustomFields(issue); cf != "" {
		fmt.Fprintf(&b, "  自定义: %s\n", cf)
	}
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

func renderJournalDetails(j Journal, attrSet map[string]bool, cfNames map[string]string) string {
	var b strings.Builder
	for _, d := range j.Details {
		name := d.Name
		if d.Property == "cf" {
			if n, ok := cfNames[d.Name]; ok {
				name = n
			}
		}
		if len(attrSet) > 0 && !attrSet[name] {
			if !(d.Property == "attachment" && attrSet["attachment"]) {
				continue
			}
		}
		switch d.Property {
		case "attr":
			fmt.Fprintf(&b, "    - 属性 [%s]: %s -> %s\n", name, fmtVal(d.OldValue), fmtVal(d.NewValue))
		case "cf":
			fmt.Fprintf(&b, "    - 自定义属性 [%s]: %s -> %s\n", name, fmtVal(d.OldValue), fmtVal(d.NewValue))
		case "attachment":
			fmt.Fprintf(&b, "    - 附件 [%s] 添加\n", name)
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
	now := time.Now()
	if !w.inWindow(now) {
		if !w.wasOffWindow {
			w.logger.Printf("当前不在监控时段(%s), 暂停监控; 回到监控时段后将补报期间发生的变更",
				w.windowDesc())
			w.wasOffWindow = true
		}
		return
	}
	if w.wasOffWindow {
		w.logger.Printf("已回到监控时段(%s), 恢复监控", w.windowDesc())
		w.wasOffWindow = false
	}

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
		full, err := w.fetchIssueJournals(issue.ID)
		if err != nil {
			w.logger.Printf("错误: 获取任务 #%d 详情失败: %v", issue.ID, err)
			continue
		}
		issue = full
		journals := issue.Journals
		sort.SliceStable(journals, func(i, j int) bool { return journals[i].ID < journals[j].ID })
		maxJID := int64(0)
		if len(journals) > 0 {
			maxJID = journals[len(journals)-1].ID
		}

		lastJID, known := w.state.SeenJournals[key]
		matched := w.issueMatches(issue)

		if !known {
			// 记录基线, 避免后续重复打印历史
			w.state.SeenJournals[key] = maxJID
			if !matched {
				continue
			}
			ts := w.toLocal(issue.CreatedOn)
			w.printBlock("新增任务", w.renderIssue(issue), "", ts)
			w.notify("新增任务", "green", issue, "", Journal{}, ts)
			continue
		}

		if !matched {
			// 不匹配过滤时跳过, 不推进 seen_journals;
			// 之后若匹配上(如指派给变更), 期间的变更会一并补报
			continue
		}

		if maxJID <= lastJID {
			continue
		}

		cfNames := cfNameMap(issue)
		assigned := false
		for _, j := range journals {
			if j.ID <= lastJID {
				continue
			}
			if !assigned && w.shouldAutoAssign(issue, j) {
				if err := w.autoAssign(issue.ID); err != nil {
					w.logger.Printf("自动指派任务 #%d 失败: %v", issue.ID, err)
				} else {
					w.logger.Printf("已自动指派任务 #%d 给自己", issue.ID)
					assigned = true
					// 重新拉取以跳过"指派变更"这条自身产生的 journal
					if full2, err := w.fetchIssueJournals(issue.ID); err == nil {
						if len(full2.Journals) > 0 {
							if m := full2.Journals[len(full2.Journals)-1].ID; m > maxJID {
								maxJID = m
							}
						}
					}
				}
			}
			if !w.journalRelevant(issue, j) {
				continue
			}
			ts := w.toLocal(j.CreatedOn)
			w.printBlock("属性变化", w.renderIssue(issue), renderJournalDetails(j, w.attrSet, cfNames), ts)
			w.notify("属性变化", "blue", issue, renderChangeCardMD(j, w.attrSet, cfNames), j, ts)
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
