package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeCardSender 记录发出的卡片, 用于测试。
type fakeCardSender struct {
	cards []map[string]interface{}
}

func (f *fakeCardSender) SendCard(card map[string]interface{}) error {
	f.cards = append(f.cards, card)
	return nil
}

func TestDailyReportTimeReached(t *testing.T) {
	day := func(h, m int) time.Time {
		return time.Date(2026, 10, 9, h, m, 0, 0, time.FixedZone("CST", 8*3600))
	}
	cases := []struct {
		now  time.Time
		hhmm string
		want bool
	}{
		{day(20, 4), "20:05", false},
		{day(20, 5), "20:05", true},
		{day(20, 6), "20:05", true},
		{day(23, 59), "20:05", true},
		{day(0, 0), "00:00", true},
		{day(0, 0), "00:01", false},
		{day(12, 0), "bad", false},
	}
	for _, c := range cases {
		if got := dailyReportTimeReached(c.now, c.hhmm); got != c.want {
			t.Errorf("dailyReportTimeReached(%v, %q) = %v, want %v", c.now, c.hhmm, got, c.want)
		}
	}
}

// reportIssue 构造用于聚合测试的任务。
func reportIssue(id int64, subject, weight string, assignee, closedOn string) Issue {
	iss := Issue{
		ID:       id,
		Subject:  subject,
		ClosedOn: closedOn,
		CustomFields: []CustomField{
			{ID: 1, Name: "统计", Value: "1"},
			{ID: 2, Name: "权重", Value: weight},
		},
	}
	if assignee != "" {
		iss.AssignedTo = &NameObj{Name: assignee}
	}
	return iss
}

func TestAggregateMonthlyReport(t *testing.T) {
	issues := []Issue{
		reportIssue(4410, "任务甲", "10", "夏 振岩", "2026-10-01T08:00:00Z"),
		reportIssue(4411, "任务乙", "42", "夏 振岩", "2026-10-05T08:00:00Z"),
		reportIssue(4412, "任务丙", "12", "祁 前", "2026-10-03T08:00:00Z"),
		reportIssue(4413, "任务丁", "0", "", ""),
		reportIssue(4414, "任务戊", "abc", "李四", "2026-10-05T08:00:00Z"), // 权重非法 → 0
	}
	rep := aggregateMonthlyReport(issues, "2026-10", "eassist")

	if rep.TotalCount != 5 {
		t.Errorf("TotalCount = %d, want 5", rep.TotalCount)
	}
	if rep.TotalWeight != 64 {
		t.Errorf("TotalWeight = %v, want 64", rep.TotalWeight)
	}
	if rep.YearMonth != "2026-10" || rep.Project != "eassist" {
		t.Errorf("YearMonth/Project = %q/%q", rep.YearMonth, rep.Project)
	}

	wantGroups := []AssigneeStat{
		{Name: "夏 振岩", Count: 2, Weight: 52},
		{Name: "祁 前", Count: 1, Weight: 12},
		{Name: "(未指派)", Count: 1, Weight: 0},
		{Name: "李四", Count: 1, Weight: 0},
	}
	if len(rep.Assignees) != len(wantGroups) {
		t.Fatalf("Assignees 长度 = %d, want %d: %+v", len(rep.Assignees), len(wantGroups), rep.Assignees)
	}
	for i, want := range wantGroups {
		got := rep.Assignees[i]
		if got != want {
			t.Errorf("Assignees[%d] = %+v, want %+v", i, got, want)
		}
	}

	// 关闭时间倒序: 10-05(4414,4411 同天按编号降序) → 10-03 → 10-01 → 无关闭时间
	wantTasks := []int64{4414, 4411, 4412, 4410, 4413}
	for i, wantID := range wantTasks {
		if rep.Tasks[i].ID != wantID {
			t.Errorf("Tasks[%d].ID = %d, want %d", i, rep.Tasks[i].ID, wantID)
		}
	}
	if rep.Tasks[0].Assignee != "李四" {
		t.Errorf("Tasks[0].Assignee = %q, want 李四", rep.Tasks[0].Assignee)
	}
	if rep.Tasks[4].Assignee != "(未指派)" {
		t.Errorf("Tasks[4].Assignee = %q, want (未指派)", rep.Tasks[4].Assignee)
	}
}

func TestAggregateMonthlyReportEmpty(t *testing.T) {
	rep := aggregateMonthlyReport(nil, "2026-10", "eassist")
	if rep.TotalCount != 0 || rep.TotalWeight != 0 || len(rep.Tasks) != 0 || len(rep.Assignees) != 0 {
		t.Errorf("空报告应全为零值: %+v", rep)
	}
}

func TestBuildReportCard(t *testing.T) {
	issues := []Issue{
		reportIssue(4411, "任务乙", "42", "夏 振岩", "2026-10-05T08:00:00Z"),
		reportIssue(4412, "任务丙", "12", "祁 前", "2026-10-03T08:00:00Z"),
	}
	rep := aggregateMonthlyReport(issues, "2026-10", "eassist")
	cfg := Config{RedmineURL: "http://redmine.example.com", DailyReport: DailyReportConfig{Project: "eassist"}}
	card := buildReportCard(rep, cfg)

	header := card["header"].(map[string]interface{})
	title := header["title"].(map[string]interface{})["content"].(string)
	if !strings.Contains(title, "2026-10") || !strings.Contains(title, "eassist") {
		t.Errorf("标题应含年月与项目: %q", title)
	}
	if header["template"] != "green" {
		t.Errorf("头部颜色 = %v, want green", header["template"])
	}

	// 汇总各 div 段落内容
	var body string
	for _, el := range card["elements"].([]map[string]interface{}) {
		if el["tag"] == "div" {
			body += el["text"].(map[string]interface{})["content"].(string) + "\n\n"
		}
	}
	for _, want := range []string{
		"**总任务:** 2",
		"**总权重:** 54",
		"**按权重分组:**",
		"- 夏 振岩: 1个 / 权重42",
		"- 祁 前: 1个 / 权重12",
		"**任务明细(按关闭时间倒序):**",
		"- #4411 [权重42] 夏 振岩 任务乙",
		"- #4412 [权重12] 祁 前 任务丙",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("卡片正文缺少 %q, 实际:\n%s", want, body)
		}
	}
}

// reportFetchHandler 模拟分页 issues.json。
type reportFetchHandler struct {
	fail    bool
	queries []url.Values
	total   int
	items   []string
}

func (h *reportFetchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/issues.json") {
		http.NotFound(w, r)
		return
	}
	if h.fail {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.queries = append(h.queries, r.URL.Query())
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page <= 0 {
		page = 1
	}
	lo := (page - 1) * 100
	hi := lo + 100
	if lo > len(h.items) {
		lo = len(h.items)
	}
	if hi > len(h.items) {
		hi = len(h.items)
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"issues":[%s],"total_count":%d}`,
		strings.Join(h.items[lo:hi], ","), h.total)
}

func reportItems(n int) []string {
	items := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		items = append(items, fmt.Sprintf(
			`{"id":%d,"subject":"任务 %d","custom_fields":[{"id":2,"name":"权重","value":"1"}]}`, i, i))
	}
	return items
}

func TestFetchMonthlyClosed(t *testing.T) {
	h := &reportFetchHandler{total: 101, items: reportItems(101)}
	srv := httptest.NewServer(h)
	defer srv.Close()

	cfg := Config{RedmineURL: srv.URL, APIKey: "k", DailyReport: DailyReportConfig{Project: "eassist"}}
	issues, err := fetchMonthlyClosed(srv.Client(), cfg)
	if err != nil {
		t.Fatalf("fetchMonthlyClosed: %v", err)
	}
	if len(issues) != 101 {
		t.Errorf("分页后总数 = %d, want 101", len(issues))
	}
	if len(h.queries) != 2 {
		t.Errorf("请求数 = %d, want 2", len(h.queries))
	}

	q := h.queries[0]
	checks := map[string]string{
		"project_id":      "eassist",
		"set_filter":      "1",
		"limit":           "100",
		"op[status_id]":   "=",
		"v[status_id][]":  "5",
		"op[closed_on]":   "m",
		"op[cf_1]":        "=",
		"v[cf_1][]":       "1",
		"op[start_date]":  "><t-",
		"v[start_date][]": "360",
	}
	for k, want := range checks {
		if got := q.Get(k); got != want {
			t.Errorf("参数 %s = %q, want %q", k, got, want)
		}
	}
	fs := q["f[]"]
	for _, want := range []string{"status_id", "closed_on", "cf_1", "start_date"} {
		found := false
		for _, f := range fs {
			if f == want {
				found = true
			}
		}
		if !found {
			t.Errorf("f[] 缺少 %q, 实际: %v", want, fs)
		}
	}
}

func TestFetchMonthlyClosedFail(t *testing.T) {
	h := &reportFetchHandler{fail: true}
	srv := httptest.NewServer(h)
	defer srv.Close()

	cfg := Config{RedmineURL: srv.URL, APIKey: "k", DailyReport: DailyReportConfig{Project: "eassist"}}
	if _, err := fetchMonthlyClosed(srv.Client(), cfg); err == nil {
		t.Error("服务端 500 时应返回错误")
	}
}

// makeReportWatcher 构造带每日报告配置与假发送方的 watcher。
func makeReportWatcher(t *testing.T, h http.Handler) (*Watcher, *fakeCardSender) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	w, _ := makeWatcher(t, srv.URL)
	w.cfg.DailyReport = DailyReportConfig{Enabled: true, Time: "20:05", Project: "eassist"}
	fake := &fakeCardSender{}
	w.feishu = fake
	return w, fake
}

func TestMaybeSendDailyReport(t *testing.T) {
	h := &mockHandler{issueJSON: reportIssueJSON(9001, "当月完成任务", "10", "夏 振岩")}
	w, fake := makeReportWatcher(t, h)
	loc := w.loc

	at := func(mo, d, hh, mm int) time.Time {
		return time.Date(2026, time.Month(mo), d, hh, mm, 0, 0, loc)
	}

	// 未到时间: 不发送
	w.maybeSendDailyReport(at(10, 9, 20, 4))
	if len(fake.cards) != 0 {
		t.Fatalf("20:04 不应发送, 实际 %d 张", len(fake.cards))
	}
	// 到时间: 发送并记录日期
	w.maybeSendDailyReport(at(10, 9, 20, 5))
	if len(fake.cards) != 1 {
		t.Fatalf("20:05 应发送 1 张, 实际 %d 张", len(fake.cards))
	}
	if w.state.LastReportDate != "2026-10-09" {
		t.Errorf("LastReportDate = %q, want 2026-10-09", w.state.LastReportDate)
	}
	// 同一天再次 tick: 去重
	w.maybeSendDailyReport(at(10, 9, 20, 30))
	w.maybeSendDailyReport(at(10, 9, 23, 59))
	if len(fake.cards) != 1 {
		t.Errorf("同一天应只发送 1 张, 实际 %d 张", len(fake.cards))
	}
	// 次日: 再发一张
	w.maybeSendDailyReport(at(10, 10, 20, 5))
	if len(fake.cards) != 2 {
		t.Errorf("次日应发送第 2 张, 实际 %d 张", len(fake.cards))
	}
	// 状态已持久化
	st := loadState(w.baseDir)
	if st.LastReportDate != "2026-10-10" {
		t.Errorf("持久化的 LastReportDate = %q, want 2026-10-10", st.LastReportDate)
	}
	// 卡片内容抽查
	header := fake.cards[0]["header"].(map[string]interface{})
	if title := header["title"].(map[string]interface{})["content"].(string); !strings.Contains(title, "2026-10") {
		t.Errorf("卡片标题缺少年月: %q", title)
	}
}

func TestMaybeSendDailyReportRetryOnFailure(t *testing.T) {
	h := &reportFetchHandler{fail: true, total: 1, items: reportItems(1)}
	w, fake := makeReportWatcher(t, h)
	now := time.Date(2026, 10, 9, 20, 5, 0, 0, w.loc)

	// 拉取失败: 不发送、不记录日期
	w.maybeSendDailyReport(now)
	if len(fake.cards) != 0 || w.state.LastReportDate != "" {
		t.Errorf("拉取失败不应发送/记录: cards=%d date=%q", len(fake.cards), w.state.LastReportDate)
	}
	// 恢复后同一时刻重试成功
	h.fail = false
	w.maybeSendDailyReport(now)
	if len(fake.cards) != 1 || w.state.LastReportDate != "2026-10-09" {
		t.Errorf("恢复后应发送成功: cards=%d date=%q", len(fake.cards), w.state.LastReportDate)
	}
}

func TestMaybeSendDailyReportDisabled(t *testing.T) {
	h := &mockHandler{issueJSON: reportIssueJSON(9001, "任务", "10", "李四")}
	w, fake := makeReportWatcher(t, h)
	w.cfg.DailyReport.Enabled = false
	w.maybeSendDailyReport(time.Date(2026, 10, 9, 20, 5, 0, 0, w.loc))
	if len(fake.cards) != 0 {
		t.Errorf("未启用时不应发送, 实际 %d 张", len(fake.cards))
	}
}

// reportIssueJSON 生成单任务 JSON(带指派者与权重)。
func reportIssueJSON(id int, subject, weight, assignee string) string {
	assigned := "null"
	if assignee != "" {
		assigned = fmt.Sprintf(`{"name":%q}`, assignee)
	}
	return fmt.Sprintf(`{"issue": {
  "id": %d,
  "subject": %q,
  "project": {"name": "eAssist"},
  "assigned_to": %s,
  "custom_fields": [{"id": 1, "name": "统计", "value": "1"}, {"id": 2, "name": "权重", "value": %q}],
  "closed_on": "2026-10-08T02:00:00Z",
  "updated_on": "2026-10-09T12:00:00Z"
}}`, id, subject, assigned, weight)
}
