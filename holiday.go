package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// holidayAPIBase timor.tech 节假日 API(测试中会替换)。
var holidayAPIBase = "http://timor.tech/api/holiday/year/"

// holidayCalendar 法定节假日日历, 按年缓存。
type holidayCalendar struct {
	mu sync.Mutex
	// year/dates: 成功拉取的年份与数据; dates 为 "2006-01-02" → 是否放假。
	year  int
	dates map[string]bool
	// failedYear/lastAttempt: 拉取失败记录, 1 小时内不重试。
	failedYear  int
	lastAttempt time.Time
}

// fetchHolidays 拉取指定年份的节假日日历。
// 返回 "2006-01-02" → true(放假) / false(调休补班)。
func fetchHolidays(client *http.Client, year int) (map[string]bool, error) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s%d", holidayAPIBase, year), nil)
	if err != nil {
		return nil, err
	}
	// 站点屏蔽 Go 默认 UA, 需自定义
	req.Header.Set("User-Agent", "redmine-auto-watch/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("节假日接口返回状态码 %d", resp.StatusCode)
	}
	var data struct {
		Code    int `json:"code"`
		Holiday map[string]struct {
			Holiday bool   `json:"holiday"`
			Date    string `json:"date"`
		} `json:"holiday"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("解析节假日数据失败: %w", err)
	}
	if data.Code != 0 {
		return nil, fmt.Errorf("节假日接口返回 code=%d", data.Code)
	}
	dates := make(map[string]bool, len(data.Holiday))
	for mmdd, h := range data.Holiday {
		key := h.Date
		if key == "" {
			key = fmt.Sprintf("%d-%s", year, mmdd)
		}
		dates[key] = h.Holiday
	}
	return dates, nil
}

// workdayFrom 判断 t 是否为法定工作日。
// 有节假日数据: 命中记录时 holiday=true(放假)→非工作日, holiday=false(调休补班)→工作日;
// 未命中按周末判断; dates 为 nil(无数据/降级)时按周一~周五判断。
func workdayFrom(dates map[string]bool, t time.Time) bool {
	if holiday, ok := dates[t.Format("2006-01-02")]; ok {
		return !holiday
	}
	return t.Weekday() != time.Saturday && t.Weekday() != time.Sunday
}

// holidayDates 返回指定年份的节假日日历; 未缓存时现场拉取, 失败后 1 小时内不重试。
// 返回 nil 表示无数据(调用方按周一~五降级)。
func (w *Watcher) holidayDates(year int) map[string]bool {
	w.cal.mu.Lock()
	defer w.cal.mu.Unlock()
	if w.cal.year == year && w.cal.dates != nil {
		return w.cal.dates
	}
	if w.cal.failedYear == year && time.Since(w.cal.lastAttempt) < time.Hour {
		return nil
	}
	w.cal.lastAttempt = time.Now()
	dates, err := fetchHolidays(w.client, year)
	if err != nil {
		w.cal.failedYear = year
		w.logger.Printf("警告: 获取 %d 年节假日数据失败(按周一~周五判断, 1 小时后重试): %v", year, err)
		return nil
	}
	w.cal.year = year
	w.cal.dates = dates
	w.cal.failedYear = 0
	return dates
}

// isLegalWorkday 判断 t 所在日期是否为法定工作日。
func (w *Watcher) isLegalWorkday(t time.Time) bool {
	return workdayFrom(w.holidayDates(t.Year()), t)
}
