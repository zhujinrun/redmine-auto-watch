package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type mockHandler struct {
	body []byte
}

func (m *mockHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(m.body)
}

func issueJSON(id int, updatedOn string, journals string) string {
	return fmt.Sprintf(`{
  "issues": [
    {
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
      "journals": [%s]
    }
  ],
  "total_count": 1
}`, id, id, updatedOn, journals)
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
	h := &mockHandler{body: []byte(issueJSON(1, "2026-08-18T12:00:00Z",
		journalJSON(10, "2026-08-18T12:00:00Z", `{"property":"attr","name":"status","old_value":"新建","new_value":"进行中"}`)))}
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

	h := &mockHandler{body: []byte(issueJSON(1, "2026-08-18T13:00:00Z",
		journalJSON(10, "2026-08-18T12:00:00Z", `{"property":"attr","name":"status","old_value":"新建","new_value":"进行中"}`)+","+
			journalJSON(11, "2026-08-18T13:00:00Z", `{"property":"attr","name":"done_ratio","old_value":"0","new_value":"50"}`)))}
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

	h := &mockHandler{body: []byte(issueJSON(1, "2026-08-18T13:00:00Z",
		journalJSON(10, "2026-08-18T12:00:00Z", `{"property":"attr","name":"status","old_value":"新建","new_value":"进行中"}`)+","+
			journalJSON(11, "2026-08-18T13:00:00Z", `{"property":"attr","name":"done_ratio","old_value":"0","new_value":"50"}`)))}
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