package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mockHandler 模拟真实 Redmine 接口行为:
// /issues.json 列表接口不返回 journals, /issues/{id}.json 单任务接口返回 journals。
type mockHandler struct {
	issueJSON string
}

func (m *mockHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(r.URL.Path, "/issues.json"):
		_, _ = w.Write(listBodyFrom(m.issueJSON))
	case strings.Contains(r.URL.Path, "/issues/"):
		_, _ = fmt.Fprint(w, m.issueJSON)
	default:
		http.NotFound(w, r)
	}
}

// listBodyFrom 从单任务 JSON 派生列表响应, 去掉 journals 字段。
func listBodyFrom(single string) []byte {
	var full struct {
		Issue map[string]interface{} `json:"issue"`
	}
	if err := json.Unmarshal([]byte(single), &full); err != nil {
		return []byte(`{"issues":[],"total_count":0}`)
	}
	delete(full.Issue, "journals")
	b, err := json.Marshal(map[string]interface{}{
		"issues":      []interface{}{full.Issue},
		"total_count": 1,
	})
	if err != nil {
		return []byte(`{"issues":[],"total_count":0}`)
	}
	return b
}

func issueJSON(id int, updatedOn string, journals string) string {
	return fmt.Sprintf(`{"issue": {
  "id": %d,
  "subject": "测试任务 %d",
  "project": {"name": "技术部"},
  "tracker": {"name": "缺陷"},
  "status": {"name": "进行中"},
  "priority": {"name": "高"},
  "author": {"name": "张三"},
  "assigned_to": {"name": "李四"},
  "description": "描述内容",
  "start_date": "2026-08-01",
  "due_date": "2026-08-10",
  "estimated_hours": 8.0,
  "spent_hours": 2.0,
  "done_ratio": 50,
  "created_on": "2026-08-01T02:00:00Z",
  "updated_on": "%s",
  "custom_fields": [{"id": 1, "name": "统计", "value": "1"}, {"id": 2, "name": "权重", "value": "10"}],
  "journals": [%s]
}}`, id, id, updatedOn, journals)
}

func journalJSON(id int, createdOn string, details string) string {
	notes := ""
	if details == "" {
		notes = `, "notes": ""`
	}
	return fmt.Sprintf(`{
      "id": %d,
      "user": {"name": "王五"},
      "notes": "测试备注",
      "created_on": "%s",
      "details": [%s]%s
    }`, id, createdOn, details, notes)
}

func makeWatcher(t *testing.T, url string) (*Watcher, *bytes.Buffer) {
	t.Helper()
	baseDir := t.TempDir()
	buf := &bytes.Buffer{}
	logger := &Logger{Logger: log.New(io.MultiWriter(buf, io.Discard), "", log.LstdFlags)}
	cfg := Config{
		RedmineURL:   url,
		APIKey:       "test-key",
		PollInterval: 30,
		LogFile:      "watch.log",
		StateFile:    "state.json",
		Timezone:     "Asia/Shanghai",
	}
	w := newWatcher(cfg, baseDir, logger)
	return w, buf
}

func TestNewIssue(t *testing.T) {
	h := &mockHandler{issueJSON: issueJSON(1, "2026-08-18T12:00:00Z",
		journalJSON(10, "2026-08-18T12:00:00Z", `{"property":"attr","name":"status","old_value":"新建","new_value":"进行中"}`))}
	srv := httptest.NewServer(h)
	defer srv.Close()

	w, buf := makeWatcher(t, srv.URL)
	w.processOnce()

	out := buf.String()
	if !strings.Contains(out, "[新增任务] #1") {
		t.Fatalf("应打印新增任务, 实际输出:\n%s", out)
	}
	if w.state.SeenJournals["1"] != 10 {
		t.Fatalf("seen_journals 应为 10, 实际 %d", w.state.SeenJournals["1"])
	}
	_ = os.RemoveAll(filepath.Join(w.baseDir, "state.json"))
}

func TestAttributeChange(t *testing.T) {
	baseDir := t.TempDir()
	logger := &Logger{Logger: log.New(io.Discard, "", log.LstdFlags)}
	cfg := Config{RedmineURL: "http://placeholder", APIKey: "k", PollInterval: 30, LogFile: "watch.log", StateFile: "state.json", Timezone: "Asia/Shanghai"}
	w := newWatcher(cfg, baseDir, logger)
	w.state.SeenJournals["1"] = 10

	h := &mockHandler{issueJSON: issueJSON(1, "2026-08-18T13:00:00Z",
		journalJSON(10, "2026-08-18T12:00:00Z", `{"property":"attr","name":"status","old_value":"新建","new_value":"进行中"}`)+","+
			journalJSON(11, "2026-08-18T13:00:00Z", `{"property":"attr","name":"done_ratio","old_value":"0","new_value":"50"}`))}
	srv := httptest.NewServer(h)
	defer srv.Close()
	w.cfg.RedmineURL = srv.URL

	buf := &bytes.Buffer{}
	w.logger = &Logger{Logger: log.New(buf, "", log.LstdFlags)}
	w.processOnce()

	out := buf.String()
	if !strings.Contains(out, "[属性变化] #1") {
		t.Fatalf("应打印属性变化, 实际输出:\n%s", out)
	}
	if !strings.Contains(out, "done_ratio") {
		t.Fatalf("应包含变更属性 done_ratio, 实际输出:\n%s", out)
	}
	if strings.Contains(out, "[新增任务]") {
		t.Fatalf("已知任务不应打印新增, 实际输出:\n%s", out)
	}
	if w.state.SeenJournals["1"] != 11 {
		t.Fatalf("seen_journals 应为 11, 实际 %d", w.state.SeenJournals["1"])
	}
}

func TestNoChange(t *testing.T) {
	baseDir := t.TempDir()
	logger := &Logger{Logger: log.New(io.Discard, "", log.LstdFlags)}
	cfg := Config{RedmineURL: "http://placeholder", APIKey: "k", PollInterval: 30, LogFile: "watch.log", StateFile: "state.json", Timezone: "Asia/Shanghai"}
	w := newWatcher(cfg, baseDir, logger)
	w.state.SeenJournals["1"] = 11

	h := &mockHandler{issueJSON: issueJSON(1, "2026-08-18T13:00:00Z",
		journalJSON(10, "2026-08-18T12:00:00Z", `{"property":"attr","name":"status","old_value":"新建","new_value":"进行中"}`)+","+
			journalJSON(11, "2026-08-18T13:00:00Z", `{"property":"attr","name":"done_ratio","old_value":"0","new_value":"50"}`))}
	srv := httptest.NewServer(h)
	defer srv.Close()
	w.cfg.RedmineURL = srv.URL

	buf := &bytes.Buffer{}
	w.logger = &Logger{Logger: log.New(buf, "", log.LstdFlags)}
	w.processOnce()

	if buf.Len() != 0 {
		t.Fatalf("无变化时不应有输出, 实际输出:\n%s", buf.String())
	}
}

func TestCustomFieldsDisplay(t *testing.T) {
	cfg := Config{
		Timezone: "Asia/Shanghai",
		Filter:   FilterConfig{CheckboxFields: []string{"统计"}},
	}
	w := newWatcher(cfg, t.TempDir(), testLogger())
	issue := Issue{
		ID: 1, Subject: "示例",
		Project: NameObj{Name: "eAssist"}, Tracker: NameObj{Name: "任务"},
		Status: NameObj{Name: "进行中"}, Priority: NameObj{Name: "高"},
		Author: NameObj{Name: "张三"}, DoneRatio: 0,
		CustomFields: []CustomField{
			{ID: 1, Name: "权重", Value: "10"},
			{ID: 2, Name: "统计", Value: "1"},
		},
	}
	cfLine := "自定义: 权重: 10 | 统计: 是"

	out := w.renderIssue(issue)
	if !strings.Contains(out, cfLine) {
		t.Fatalf("控制台应显示自定义字段, 实际:\n%s", out)
	}

	md := w.renderIssueCardMD(issue)
	if !strings.Contains(md, "**自定义:** 权重: 10 | 统计: 是") {
		t.Fatalf("飞书卡片应显示自定义字段, 实际:\n%s", md)
	}
}

func TestProcessOnceShowsCustomFields(t *testing.T) {
	baseDir := t.TempDir()
	cfg := Config{RedmineURL: "http://placeholder", APIKey: "k", PollInterval: 30, LogFile: "watch.log", StateFile: "state.json", Timezone: "Asia/Shanghai"}
	w := newWatcher(cfg, baseDir, testLogger())

	h := &mockHandler{issueJSON: issueJSON(1, "2026-08-18T13:00:00Z",
		journalJSON(10, "2026-08-18T12:00:00Z", `{"property":"attr","name":"status","old_value":"新建","new_value":"进行中"}`))}
	srv := httptest.NewServer(h)
	defer srv.Close()
	w.cfg.RedmineURL = srv.URL

	buf := &bytes.Buffer{}
	w.logger = &Logger{Logger: log.New(buf, "", log.LstdFlags)}
	w.processOnce()

	out := buf.String()
	if !strings.Contains(out, "自定义: 统计: 1 | 权重: 10") {
		t.Fatalf("新增任务详情应包含自定义字段, 实际输出:\n%s", out)
	}
}

func testLogger() *Logger {
	return &Logger{Logger: log.New(io.Discard, "", log.LstdFlags)}
}

func TestInWindow(t *testing.T) {
	cfg := Config{
		Timezone: "Asia/Shanghai",
		Filter: FilterConfig{
			MonitorWeekdays: []int{1, 2, 3, 4, 5},
			MonitorStart:    "09:00",
			MonitorEnd:      "18:00",
		},
	}
	w := newWatcher(cfg, t.TempDir(), testLogger())
	loc := w.loc

	cases := []struct {
		when time.Time
		want bool
		desc string
	}{
		{time.Date(2026, 8, 17, 10, 0, 0, 0, loc), true, "周一 10:00"},
		{time.Date(2026, 8, 17, 8, 59, 0, 0, loc), false, "周一 08:59 (未到开始)"},
		{time.Date(2026, 8, 17, 18, 0, 0, 0, loc), false, "周一 18:00 (含头不含尾)"},
		{time.Date(2026, 8, 22, 10, 0, 0, 0, loc), false, "周六 10:00 (非工作日)"},
		{time.Date(2026, 8, 23, 10, 0, 0, 0, loc), false, "周日 10:00 (非工作日)"},
	}
	for _, c := range cases {
		if got := w.inWindow(c.when); got != c.want {
			t.Errorf("%s: inWindow=%v, 期望 %v", c.desc, got, c.want)
		}
	}
}

func TestInWindowCrossDay(t *testing.T) {
	cfg := Config{
		Timezone: "Asia/Shanghai",
		Filter: FilterConfig{
			MonitorStart: "22:00",
			MonitorEnd:   "06:00",
		},
	}
	w := newWatcher(cfg, t.TempDir(), testLogger())
	loc := w.loc

	if got := w.inWindow(time.Date(2026, 8, 17, 23, 0, 0, 0, loc)); !got {
		t.Error("23:00 应在跨天监控时段内")
	}
	if got := w.inWindow(time.Date(2026, 8, 18, 5, 30, 0, 0, loc)); !got {
		t.Error("次日 05:30 应在跨天监控时段内")
	}
	if got := w.inWindow(time.Date(2026, 8, 18, 12, 0, 0, 0, loc)); got {
		t.Error("12:00 不应在跨天监控时段内")
	}
}

func TestIssueMatches(t *testing.T) {
	cfg := Config{
		Timezone: "Asia/Shanghai",
		Filter: FilterConfig{
			Projects:  []string{"技术部"},
			Assignees: []string{"张三"},
		},
	}
	w := newWatcher(cfg, t.TempDir(), testLogger())

	issue := Issue{Project: NameObj{Name: "技术部"}, AssignedTo: &NameObj{Name: "张三"}}
	if !w.issueMatches(issue) {
		t.Error("技术部+张三 应匹配")
	}
	issue.Project.Name = "运维部"
	if w.issueMatches(issue) {
		t.Error("项目不匹配应排除")
	}
	issue.Project.Name = "技术部"
	issue.AssignedTo.Name = "李四"
	if w.issueMatches(issue) {
		t.Error("指派给不匹配应排除")
	}
	issue.AssignedTo = nil
	if w.issueMatches(issue) {
		t.Error("未指派任务应排除")
	}
}

func TestIssueMatchesUnassigned(t *testing.T) {
	cfg := Config{
		Timezone: "Asia/Shanghai",
		Filter: FilterConfig{
			Assignees: []string{"unassigned"},
		},
	}
	w := newWatcher(cfg, t.TempDir(), testLogger())

	unassigned := Issue{Project: NameObj{Name: "任意"}, AssignedTo: nil}
	if !w.issueMatches(unassigned) {
		t.Error("未指派任务应匹配 unassigned")
	}
	assigned := Issue{Project: NameObj{Name: "任意"}, AssignedTo: &NameObj{Name: "张三"}}
	if w.issueMatches(assigned) {
		t.Error("已指派任务不应匹配 unassigned")
	}
}

func TestNewSchemaAttributeMatch(t *testing.T) {
	baseDir := t.TempDir()
	cfg := Config{
		RedmineURL:   "http://placeholder",
		APIKey:       "k",
		PollInterval: 30,
		LogFile:      "watch.log",
		StateFile:    "state.json",
		Timezone:     "Asia/Shanghai",
		Filter:       FilterConfig{Attributes: []string{"assigned_to", "权重"}},
	}
	w := newWatcher(cfg, baseDir, testLogger())
	w.state.SeenJournals["1"] = 10

	// 新版 Redmine schema:
	// 指派变化 detail.name=assigned_to_id (值为用户ID), 清空指派时 new 为空;
	// 自定义字段变化 detail.name=字段ID (2=权重), property=cf。
	j11 := journalJSON(11, "2026-08-18T13:00:00Z",
		`{"property":"attr","name":"assigned_to_id","old_value":"5","new_value":""},`+
			`{"property":"cf","name":"2","old_value":"1","new_value":"10"}`)
	h := &mockHandler{issueJSON: issueJSON(1, "2026-08-18T13:00:00Z", j11)}
	srv := httptest.NewServer(h)
	defer srv.Close()
	w.cfg.RedmineURL = srv.URL

	buf := &bytes.Buffer{}
	w.logger = &Logger{Logger: log.New(buf, "", log.LstdFlags)}
	w.processOnce()

	out := buf.String()
	if !strings.Contains(out, "属性 [assigned_to_id]: 5 -> (空)") {
		t.Fatalf("应报告指派清空变化, 实际输出:\n%s", out)
	}
	if !strings.Contains(out, "自定义属性 [权重]: 1 -> 10") {
		t.Fatalf("应报告自定义字段 权重 变化, 实际输出:\n%s", out)
	}
	if w.state.SeenJournals["1"] != 11 {
		t.Fatalf("seen_journals 应为 11, 实际 %d", w.state.SeenJournals["1"])
	}
}

// autoAssignHandler 模拟: 提供 /users/current.json, 记录对任务发起的 PUT(自动指派)。
type autoAssignHandler struct {
	issueJSON string
	putCount  int
	putBodies []string
}

func (m *autoAssignHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(r.URL.Path, "/issues.json"):
		_, _ = w.Write(listBodyFrom(m.issueJSON))
	case strings.HasSuffix(r.URL.Path, "/users/current.json"):
		_, _ = fmt.Fprint(w, `{"user":{"id":5}}`)
	case strings.Contains(r.URL.Path, "/issues/"):
		if r.Method == http.MethodPut {
			b, _ := io.ReadAll(r.Body)
			m.putCount++
			m.putBodies = append(m.putBodies, string(b))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = fmt.Fprint(w, m.issueJSON)
	default:
		http.NotFound(w, r)
	}
}

func TestAutoAssignUnassigned(t *testing.T) {
	baseDir := t.TempDir()

	// 未指派任务, 权重(cf id=2) 从 1 变为 2
	j11 := journalJSON(11, "2026-08-19T00:00:00Z",
		`{"property":"cf","name":"2","old_value":"1","new_value":"2"}`)
	issueBody := fmt.Sprintf(`{"issue": {
  "id": 1,
  "subject": "待指派任务",
  "project": {"name": "eAssist"},
  "tracker": {"name": "任务"},
  "status": {"name": "进行中"},
  "priority": {"name": "高"},
  "author": {"name": "朱 锦润"},
  "description": "",
  "start_date": "2026-08-19",
  "done_ratio": 0,
  "created_on": "2026-08-19T00:00:00Z",
  "updated_on": "2026-08-19T00:00:00Z",
  "custom_fields": [{"id": 1, "name": "统计", "value": "1"}, {"id": 2, "name": "权重", "value": "2"}],
  "journals": [%s]
}}`, j11)

	h := &autoAssignHandler{issueJSON: issueBody}
	srv := httptest.NewServer(h)
	defer srv.Close()

	cfg := Config{
		RedmineURL:   srv.URL,
		APIKey:       "k",
		PollInterval: 30,
		LogFile:      "watch.log",
		StateFile:    "state.json",
		Timezone:     "Asia/Shanghai",
		AutoAssign:   AutoAssignConfig{Enabled: true, CustomField: "权重", FromValue: "1"},
	}
	w := newWatcher(cfg, baseDir, testLogger())
	w.state.SeenJournals["1"] = 10

	buf := &bytes.Buffer{}
	w.logger = &Logger{Logger: log.New(buf, "", log.LstdFlags)}
	w.processOnce()

	if h.putCount != 1 {
		t.Fatalf("应发起 1 次自动指派 PUT, 实际 %d", h.putCount)
	}
	if !strings.Contains(h.putBodies[0], `"assigned_to_id":5`) {
		t.Fatalf("PUT 应指派给用户 id=5, 实际请求体: %s", h.putBodies[0])
	}
	out := buf.String()
	if !strings.Contains(out, "已自动指派任务 #1 给自己") {
		t.Fatalf("应记录自动指派日志, 实际输出:\n%s", out)
	}
	if !strings.Contains(out, "[属性变化]") {
		t.Fatalf("权重变化仍应正常报告, 实际输出:\n%s", out)
	}
}

func TestAutoAssignNotTriggerWhenAssigned(t *testing.T) {
	baseDir := t.TempDir()

	// 已指派的任务(李四), 权重从 1 变 2: 不应自动指派
	j11 := journalJSON(11, "2026-08-19T00:00:00Z",
		`{"property":"cf","name":"2","old_value":"1","new_value":"2"}`)
	h := &mockHandler{issueJSON: issueJSON(1, "2026-08-19T00:00:00Z", j11)}
	srv := httptest.NewServer(h)
	defer srv.Close()

	cfg := Config{
		RedmineURL:   srv.URL,
		APIKey:       "k",
		PollInterval: 30,
		LogFile:      "watch.log",
		StateFile:    "state.json",
		Timezone:     "Asia/Shanghai",
		AutoAssign:   AutoAssignConfig{Enabled: true, CustomField: "权重", FromValue: "1"},
	}
	w := newWatcher(cfg, baseDir, testLogger())
	w.state.SeenJournals["1"] = 10

	buf := &bytes.Buffer{}
	w.logger = &Logger{Logger: log.New(buf, "", log.LstdFlags)}
	w.processOnce()

	if strings.Contains(buf.String(), "已自动指派") {
		t.Fatalf("已指派任务不应自动指派, 实际输出:\n%s", buf.String())
	}
}

func TestAttributeFilter(t *testing.T) {
	baseDir := t.TempDir()
	cfg := Config{
		RedmineURL:   "http://placeholder",
		APIKey:       "k",
		PollInterval: 30,
		LogFile:      "watch.log",
		StateFile:    "state.json",
		Timezone:     "Asia/Shanghai",
		Filter:       FilterConfig{Attributes: []string{"status"}},
	}
	w := newWatcher(cfg, baseDir, testLogger())
	w.state.SeenJournals["1"] = 10

	j11 := journalJSON(11, "2026-08-18T13:00:00Z",
		`{"property":"attr","name":"status","old_value":"新建","new_value":"进行中"},`+
			`{"property":"attr","name":"done_ratio","old_value":"0","new_value":"50"}`)
	h := &mockHandler{issueJSON: issueJSON(1, "2026-08-18T13:00:00Z", j11)}
	srv := httptest.NewServer(h)
	defer srv.Close()
	w.cfg.RedmineURL = srv.URL

	buf := &bytes.Buffer{}
	w.logger = &Logger{Logger: log.New(buf, "", log.LstdFlags)}
	w.processOnce()

	out := buf.String()
	if !strings.Contains(out, "属性 [status]") {
		t.Fatalf("应报告 status 变化, 实际输出:\n%s", out)
	}
	if strings.Contains(out, "done_ratio") {
		t.Fatalf("不应报告 done_ratio(未配置), 实际输出:\n%s", out)
	}
}

func TestCustomFieldFilterAndRender(t *testing.T) {
	baseDir := t.TempDir()
	cfg := Config{
		RedmineURL:   "http://placeholder",
		APIKey:       "k",
		PollInterval: 30,
		LogFile:      "watch.log",
		StateFile:    "state.json",
		Timezone:     "Asia/Shanghai",
		Filter:       FilterConfig{Attributes: []string{"客户"}},
	}
	w := newWatcher(cfg, baseDir, testLogger())
	w.state.SeenJournals["1"] = 10

	// 自定义字段变化 property=cf, 名称=客户; 同时有一个 status 的标准属性变化
	j11 := journalJSON(11, "2026-08-18T13:00:00Z",
		`{"property":"cf","name":"客户","old_value":"A公司","new_value":"B公司"},`+
			`{"property":"attr","name":"status","old_value":"新建","new_value":"进行中"}`)
	h := &mockHandler{issueJSON: issueJSON(1, "2026-08-18T13:00:00Z", j11)}
	srv := httptest.NewServer(h)
	defer srv.Close()
	w.cfg.RedmineURL = srv.URL

	buf := &bytes.Buffer{}
	w.logger = &Logger{Logger: log.New(buf, "", log.LstdFlags)}
	w.processOnce()

	out := buf.String()
	if !strings.Contains(out, "自定义属性 [客户]: A公司 -> B公司") {
		t.Fatalf("应报告自定义属性 客户 变化, 实际输出:\n%s", out)
	}
	if strings.Contains(out, "属性 [status]") {
		t.Fatalf("不应报告 status(未配置), 实际输出:\n%s", out)
	}
}