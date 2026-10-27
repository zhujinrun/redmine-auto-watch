package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// cardSender 飞书卡片发送方, 抽象为接口便于测试注入。
type cardSender interface {
	SendCard(card map[string]interface{}) error
}

// AssigneeStat 按指派给分组的统计。
type AssigneeStat struct {
	Name   string
	Count  int
	Weight float64
}

// ReportTask 单个已完成任务(用于明细)。
type ReportTask struct {
	ID       int64
	Subject  string
	Weight   float64
	Assignee string // 指派给, 未指派为 "(未指派)"
	ClosedOn string
}

// MonthlyReport 当月已完成任务统计。
type MonthlyReport struct {
	YearMonth   string // "2006-01"
	Project     string
	TotalCount  int
	TotalWeight float64
	Assignees   []AssigneeStat // 按总权重降序
	Tasks       []ReportTask   // 按关闭时间倒序, 同时间按编号降序
}

// dailyReportTimeReached 判断 now(本地) 是否已到当天的 "HH:MM"。
func dailyReportTimeReached(now time.Time, hhmm string) bool {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return false
	}
	nowMin := now.Hour()*60 + now.Minute()
	return nowMin >= t.Hour()*60+t.Minute()
}

// fetchMonthlyClosed 拉取项目内当月已关闭且"统计=1"的任务。
// 过滤条件与 Redmine 统计页面完全一致:
//
//	status_id=5(已关闭) + closed_on op=m(当月) + cf_1=1(统计=是) + start_date ><t-360
func fetchMonthlyClosed(client *http.Client, cfg Config) ([]Issue, error) {
	var all []Issue
	for page := 1; ; page++ {
		params := url.Values{}
		params.Set("project_id", cfg.DailyReport.Project)
		params.Set("limit", "100")
		params.Set("page", fmt.Sprintf("%d", page))
		params.Set("sort", "closed_on:desc")
		params.Set("set_filter", "1")
		params.Add("f[]", "status_id")
		params.Add("op[status_id]", "=")
		params.Add("v[status_id][]", "5")
		params.Add("f[]", "closed_on")
		params.Add("op[closed_on]", "m")
		params.Add("f[]", "cf_1")
		params.Add("op[cf_1]", "=")
		params.Add("v[cf_1][]", "1")
		params.Add("f[]", "start_date")
		params.Add("op[start_date]", "><t-")
		params.Add("v[start_date][]", "360")

		body, err := redmineGetWith(client, cfg, "/issues.json", params)
		if err != nil {
			return nil, err
		}
		var data issuesResponse
		if err := json.Unmarshal(body, &data); err != nil {
			return nil, fmt.Errorf("解析 issues.json 失败: %w", err)
		}
		all = append(all, data.Issues...)
		if page*100 >= data.TotalCount {
			break
		}
	}
	return all, nil
}

// issueWeight 取任务的"权重"自定义字段值, 非法/缺失记 0。
func issueWeight(issue Issue) float64 {
	for _, cf := range issue.CustomFields {
		if cf.Name == "权重" {
			if f, err := strconv.ParseFloat(strings.TrimSpace(fmtVal(cf.Value)), 64); err == nil {
				return f
			}
		}
	}
	return 0
}

// aggregateMonthlyReport 汇总当月已完成任务。
func aggregateMonthlyReport(issues []Issue, yearMonth, project string) MonthlyReport {
	rep := MonthlyReport{YearMonth: yearMonth, Project: project, TotalCount: len(issues)}
	groups := map[string]*AssigneeStat{}
	for _, issue := range issues {
		w := issueWeight(issue)
		rep.TotalWeight += w
		name := "(未指派)"
		if issue.AssignedTo != nil && strings.TrimSpace(issue.AssignedTo.Name) != "" {
			name = issue.AssignedTo.Name
		}
		g := groups[name]
		if g == nil {
			g = &AssigneeStat{Name: name}
			groups[name] = g
		}
		g.Count++
		g.Weight += w
		rep.Tasks = append(rep.Tasks, ReportTask{ID: issue.ID, Subject: issue.Subject, Weight: w, Assignee: name, ClosedOn: issue.ClosedOn})
	}
	for _, g := range groups {
		rep.Assignees = append(rep.Assignees, *g)
	}
	sort.Slice(rep.Assignees, func(i, j int) bool {
		a, b := rep.Assignees[i], rep.Assignees[j]
		if a.Weight != b.Weight {
			return a.Weight > b.Weight
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Name < b.Name
	})
	sort.Slice(rep.Tasks, func(i, j int) bool {
		if rep.Tasks[i].ClosedOn != rep.Tasks[j].ClosedOn {
			return rep.Tasks[i].ClosedOn > rep.Tasks[j].ClosedOn
		}
		return rep.Tasks[i].ID > rep.Tasks[j].ID
	})
	return rep
}

// fmtWeight 格式化权重: 整数不带小数点。
func fmtWeight(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// buildReportCard 生成每日报告的飞书卡片。
func buildReportCard(rep MonthlyReport, cfg Config) map[string]interface{} {
	title := fmt.Sprintf("%s 当月已完成任务统计 · %s", rep.YearMonth, rep.Project)

	var summary strings.Builder
	fmt.Fprintf(&summary, "**总任务:** %d | **总权重:** %s", rep.TotalCount, fmtWeight(rep.TotalWeight))

	var groups strings.Builder
	groups.WriteString("**按权重分组:**\n")
	if len(rep.Assignees) == 0 {
		groups.WriteString("- (无)")
	}
	for i, g := range rep.Assignees {
		if i > 0 {
			groups.WriteString("\n")
		}
		fmt.Fprintf(&groups, "- %s: %d个 / 权重%s", g.Name, g.Count, fmtWeight(g.Weight))
	}

	var tasks strings.Builder
	tasks.WriteString("**任务明细(按关闭时间倒序):**\n")
	if len(rep.Tasks) == 0 {
		tasks.WriteString("- (无)")
	}
	for i, t := range rep.Tasks {
		if i > 0 {
			tasks.WriteString("\n")
		}
		fmt.Fprintf(&tasks, "- #%d [权重%s] %s %s", t.ID, fmtWeight(t.Weight), t.Assignee, truncate(t.Subject, 60))
	}

	body := summary.String() + "\n\n" + groups.String() + "\n\n" + tasks.String()
	note := "生成时间: " + time.Now().In(loadLoc(cfg.Timezone)).Format("2006-01-02 15:04:05") +
		" | 过滤: 当月已关闭 & 统计=是"
	issuesURL := strings.TrimRight(cfg.RedmineURL, "/") + "/projects/" + cfg.DailyReport.Project + "/issues"
	return buildCard(title, body, note, issuesURL, "green")
}

// maybeSendDailyReport 到达每日报告时间且当天未发送时, 生成并发送当月已完成任务统计。
func (w *Watcher) maybeSendDailyReport(now time.Time) {
	dr := w.cfg.DailyReport
	if !dr.Enabled || w.feishu == nil {
		return
	}
	local := now.In(w.loc)
	today := local.Format("2006-01-02")
	if w.state.LastReportDate == today {
		return
	}
	if !dailyReportTimeReached(local, dr.Time) {
		return
	}
	// 只在法定工作日发送: 法定节假日/周末跳过, 调休补班日照常发送。
	// 跳过时不记录日期, 到下个工作日正常发送。
	if dr.WorkdaysOnly && !w.isLegalWorkday(local) {
		return
	}
	issues, err := fetchMonthlyClosed(w.client, w.cfg)
	if err != nil {
		w.logger.Printf("每日报告获取任务失败: %v", err)
		return
	}
	rep := aggregateMonthlyReport(issues, local.Format("2006-01"), dr.Project)
	if err := w.feishu.SendCard(buildReportCard(rep, w.cfg)); err != nil {
		w.logger.Printf("每日报告发送失败: %v", err)
		return
	}
	w.state.LastReportDate = today
	if err := saveState(w.baseDir, w.state); err != nil {
		w.logger.Printf("警告: 保存状态失败: %v", err)
	}
	w.logger.Printf("每日报告已发送: %s 总任务=%d 总权重=%s",
		rep.YearMonth, rep.TotalCount, fmtWeight(rep.TotalWeight))
}
