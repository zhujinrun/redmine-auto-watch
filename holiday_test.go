package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkdayFrom(t *testing.T) {
	// 2026-10-01 周四 / 10-07 周三 / 10-09 周五 / 10-10 周六 / 10-11 周日
	dates := map[string]bool{
		"2026-10-01": true,  // 国庆放假(工作日放假 → 非工作日)
		"2026-10-07": true,  // 假期中的周三 → 非工作日
		"2026-10-10": false, // 周六调休补班 → 工作日
	}
	day := func(m, d int) time.Time {
		return time.Date(2026, time.Month(m), d, 12, 0, 0, 0, time.FixedZone("CST", 8*3600))
	}
	cases := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"有记录-放假的周四", day(10, 1), false},
		{"有记录-放假的周三", day(10, 7), false},
		{"有记录-补班的周六", day(10, 10), true},
		{"无记录-周五", day(10, 9), true},
		{"无记录-周日", day(10, 11), false},
		{"无记录-周一", day(10, 12), true},
	}
	for _, c := range cases {
		if got := workdayFrom(dates, c.t); got != c.want {
			t.Errorf("%s: workdayFrom = %v, want %v", c.name, got, c.want)
		}
	}

	// 无数据(降级)时按周一~周五
	for d := 1; d <= 7; d++ {
		tt := time.Date(2026, 11, d, 12, 0, 0, 0, time.UTC)
		want := tt.Weekday() != time.Saturday && tt.Weekday() != time.Sunday
		if got := workdayFrom(nil, tt); got != want {
			t.Errorf("降级 %s: got %v, want %v", tt.Weekday(), got, want)
		}
	}
}

func TestFetchHolidays(t *testing.T) {
	setHolidayAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"holiday":{
			"10-01":{"holiday":true,"date":"2026-10-01","name":"国庆节"},
			"10-10":{"holiday":false,"date":"2026-10-10","name":"国庆节后补班"},
			"01-02":{"holiday":true,"name":"元旦"}
		}}`)
	}))

	dates, err := fetchHolidays(http.DefaultClient, 2026)
	if err != nil {
		t.Fatalf("fetchHolidays: %v", err)
	}
	want := map[string]bool{
		"2026-10-01": true,
		"2026-10-10": false,
		"2026-01-02": true, // 无 date 字段时回退为 年-MM-DD
	}
	for k, v := range want {
		if got, ok := dates[k]; !ok || got != v {
			t.Errorf("dates[%q] = %v,%v, want %v", k, got, ok, v)
		}
	}
	if len(dates) != 3 {
		t.Errorf("dates 长度 = %d, want 3", len(dates))
	}
}

func TestFetchHolidaysErrors(t *testing.T) {
	t.Run("非0 code", func(t *testing.T) {
		setHolidayAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"code":1}`)
		}))
		if _, err := fetchHolidays(http.DefaultClient, 2026); err == nil {
			t.Error("code!=0 应返回错误")
		}
	})
	t.Run("HTTP 500", func(t *testing.T) {
		setHolidayAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		if _, err := fetchHolidays(http.DefaultClient, 2026); err == nil {
			t.Error("500 应返回错误")
		}
	})
	t.Run("非法 JSON", func(t *testing.T) {
		setHolidayAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `not json`)
		}))
		if _, err := fetchHolidays(http.DefaultClient, 2026); err == nil {
			t.Error("非法 JSON 应返回错误")
		}
	})
}

// setHolidayAPI 替换节假日 API 地址, 测试结束恢复。
func setHolidayAPI(t *testing.T, handler http.Handler) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	old := holidayAPIBase
	holidayAPIBase = srv.URL + "/"
	t.Cleanup(func() { holidayAPIBase = old })
}

func TestIsLegalWorkdayCache(t *testing.T) {
	var hits int32
	setHolidayAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		fmt.Fprint(w, `{"code":0,"holiday":{
			"10-01":{"holiday":true,"date":"2026-10-01"},
			"10-10":{"holiday":false,"date":"2026-10-10"}
		}}`)
	}))
	w, _ := makeWatcher(t, "http://redmine.invalid")
	day := func(m, d int) time.Time {
		return time.Date(2026, time.Month(m), d, 12, 0, 0, 0, w.loc)
	}
	checks := []struct {
		t    time.Time
		want bool
	}{
		{day(10, 1), false},  // 放假
		{day(10, 10), true},  // 调休补班
		{day(10, 9), true},   // 普通周五
		{day(10, 11), false}, // 普通周日
	}
	for _, c := range checks {
		if got := w.isLegalWorkday(c.t); got != c.want {
			t.Errorf("isLegalWorkday(%s) = %v, want %v", c.t.Format("2006-01-02"), got, c.want)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("节假日 API 应只请求 1 次(按年缓存), 实际 %d 次", n)
	}
}

func TestIsLegalWorkdayFallback(t *testing.T) {
	var hits int32
	setHolidayAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	w, _ := makeWatcher(t, "http://redmine.invalid")

	// 降级为周一~周五判断
	wed := time.Date(2026, 11, 4, 12, 0, 0, 0, w.loc) // 周三
	sat := time.Date(2026, 11, 7, 12, 0, 0, 0, w.loc) // 周六
	if wed.Weekday() != time.Wednesday || sat.Weekday() != time.Saturday {
		t.Fatalf("测试日期星期不对: %s %s", wed.Weekday(), sat.Weekday())
	}
	if !w.isLegalWorkday(wed) {
		t.Error("降级模式周三应为工作日")
	}
	if w.isLegalWorkday(sat) {
		t.Error("降级模式周六应为非工作日")
	}
	// 失败后 1 小时内不重试 → 仍只有 1 次请求
	w.isLegalWorkday(wed)
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("失败退避期内不应重复请求, 实际 %d 次", n)
	}
}

func TestMaybeSendDailyReportWorkdaysOnly(t *testing.T) {
	h := &mockHandler{issueJSON: reportIssueJSON(9001, "当月完成任务", "10", "夏 振岩")}
	w, fake := makeReportWatcher(t, h)
	w.cfg.DailyReport.WorkdaysOnly = true

	// 10-09(周五)放假, 10-10(周六)调休补班, 10-11(周日)无记录
	setHolidayAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"holiday":{
			"10-09":{"holiday":true,"date":"2026-10-09"},
			"10-10":{"holiday":false,"date":"2026-10-10"}
		}}`)
	}))

	at := func(d, hh, mm int) time.Time {
		return time.Date(2026, 10, d, hh, mm, 0, 0, w.loc)
	}

	// 放假的工作日: 跳过
	w.maybeSendDailyReport(at(9, 20, 5))
	if len(fake.cards) != 0 {
		t.Errorf("放假日不应发送, 实际 %d 张", len(fake.cards))
	}
	// 调休补班的周六: 照常发送
	w.maybeSendDailyReport(at(10, 20, 5))
	if len(fake.cards) != 1 {
		t.Errorf("补班日应发送 1 张, 实际 %d 张", len(fake.cards))
	}
	// 普通周日: 跳过
	w.maybeSendDailyReport(at(11, 20, 5))
	if len(fake.cards) != 1 {
		t.Errorf("周日不应发送, 实际 %d 张", len(fake.cards))
	}
	// 未到时间不发(工作日 20:04)
	w.maybeSendDailyReport(at(12, 20, 4))
	if len(fake.cards) != 1 {
		t.Errorf("20:04 不应发送, 实际 %d 张", len(fake.cards))
	}
}
