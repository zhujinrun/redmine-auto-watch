package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const defaultFeishuAPIBase = "https://open.feishu.cn"

type FeishuClient struct {
	apiBase       string
	appID         string
	appSecret     string
	receiveIDType string
	receiveID     string
	client        *http.Client

	mu          sync.Mutex
	token       string
	tokenExpire time.Time
}

func newFeishuClient(cfg FeishuConfig) *FeishuClient {
	base := cfg.APIBase
	if base == "" {
		base = defaultFeishuAPIBase
	}
	ridType := cfg.ReceiveIDType
	if ridType == "" {
		ridType = "open_id"
	}
	return &FeishuClient{
		apiBase:       strings.TrimRight(base, "/"),
		appID:         cfg.AppID,
		appSecret:     cfg.AppSecret,
		receiveIDType: ridType,
		receiveID:     cfg.ReceiveID,
		client:        &http.Client{Timeout: 15 * time.Second},
	}
}

type tokenResponse struct {
	Code              int    `json:"code"`
	Msg               string `json:"msg"`
	TenantAccessToken string `json:"tenant_access_token"`
	Expire            int    `json:"expire"`
}

func (f *FeishuClient) getToken() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.token != "" && time.Now().Before(f.tokenExpire.Add(-5*time.Minute)) {
		return f.token, nil
	}

	body, _ := json.Marshal(map[string]string{
		"app_id":     f.appID,
		"app_secret": f.appSecret,
	})
	resp, err := f.client.Post(f.apiBase+"/open-apis/auth/v3/tenant_access_token/internal",
		"application/json; charset=utf-8", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("请求获取 token 失败: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	var tr tokenResponse
	if err := json.Unmarshal(data, &tr); err != nil {
		return "", fmt.Errorf("解析 token 响应失败: %w", err)
	}
	if tr.Code != 0 || tr.TenantAccessToken == "" {
		return "", fmt.Errorf("获取 tenant_access_token 失败: code=%d msg=%s", tr.Code, tr.Msg)
	}
	f.token = tr.TenantAccessToken
	expire := tr.Expire
	if expire <= 0 {
		expire = 7200
	}
	f.tokenExpire = time.Now().Add(time.Duration(expire) * time.Second)
	return f.token, nil
}

type messageResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func (f *FeishuClient) postMessage(token string, payload []byte) (messageResponse, error) {
	u := fmt.Sprintf("%s/open-apis/im/v1/messages?receive_id_type=%s",
		f.apiBase, f.receiveIDType)
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return messageResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := f.client.Do(req)
	if err != nil {
		return messageResponse{}, fmt.Errorf("发送消息失败: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	var mr messageResponse
	if err := json.Unmarshal(data, &mr); err != nil {
		return mr, fmt.Errorf("解析发送响应失败: %s", truncate(string(data), 300))
	}
	return mr, nil
}

func (f *FeishuClient) SendCard(card map[string]interface{}) error {
	cardJSON, err := json.Marshal(card)
	if err != nil {
		return fmt.Errorf("序列化卡片失败: %w", err)
	}
	payload, _ := json.Marshal(map[string]string{
		"receive_id": f.receiveID,
		"msg_type":   "interactive",
		"content":    string(cardJSON),
	})

	token, err := f.getToken()
	if err != nil {
		return err
	}

	// 首次发送; 若返回 99991663(token 失效)/99991664 等授权错误则刷新 token 重试一次
	mr, err := f.postMessage(token, payload)
	if err != nil {
		return err
	}
	if mr.Code != 0 {
		if mr.Code == 99991663 || mr.Code == 99991664 || mr.Code == 99991668 || mr.Code == 99991672 {
			f.mu.Lock()
			f.token = ""
			f.mu.Unlock()
			token, err = f.getToken()
			if err != nil {
				return err
			}
			mr, err = f.postMessage(token, payload)
			if err != nil {
				return err
			}
		}
		if mr.Code != 0 {
			return fmt.Errorf("飞书发送失败: code=%d msg=%s", mr.Code, mr.Msg)
		}
	}
	return nil
}

// buildCard 构造富文本交互卡片。
// color: 头部模板色, 如 "blue"/"green"/"red"/"orange"。
func buildCard(title, mdBody, note, issueURL, color string) map[string]interface{} {
	elements := []map[string]interface{}{}
	for _, section := range splitSections(mdBody) {
		elements = append(elements, map[string]interface{}{
			"tag":  "div",
			"text": map[string]interface{}{"tag": "lark_md", "content": section},
		})
	}
	if issueURL != "" {
		elements = append(elements,
			map[string]interface{}{
				"tag": "action",
				"actions": []map[string]interface{}{
					{
						"tag":  "button",
						"type": "primary",
						"text": map[string]interface{}{"tag": "plain_text", "content": "打开任务"},
						"url":  issueURL,
					},
				},
			})
	}
	if note != "" {
		elements = append(elements, map[string]interface{}{
			"tag": "note",
			"elements": []map[string]interface{}{
				{"tag": "plain_text", "content": note},
			},
		})
	}
	if color == "" {
		color = "blue"
	}
	return map[string]interface{}{
		"config": map[string]interface{}{"wide_screen_mode": true},
		"header": map[string]interface{}{
			"template": color,
			"title":    map[string]interface{}{"tag": "plain_text", "content": title},
		},
		"elements": elements,
	}
}

func splitSections(md string) []string {
	var sections []string
	for _, s := range strings.Split(md, "\n\n") {
		if t := strings.TrimSpace(s); t != "" {
			sections = append(sections, t)
		}
	}
	return sections
}

// renderIssueCardMD 生成卡片的任务详情 markdown。
func (w *Watcher) renderIssueCardMD(issue Issue) string {
	var b strings.Builder
	assigned := ""
	if issue.AssignedTo != nil {
		assigned = issue.AssignedTo.Name
	}
	fmt.Fprintf(&b, "**任务标题:** %s\n", fmtVal(issue.Subject))
	fmt.Fprintf(&b, "**项目:** %s | **跟踪:** %s | **状态:** %s | **优先级:** %s\n",
		issue.Project.Name, issue.Tracker.Name, issue.Status.Name, issue.Priority.Name)
	fmt.Fprintf(&b, "**指派给:** %s | **作者:** %s | **开始:** %s\n",
		assigned, issue.Author.Name, fmtVal(issue.StartDate))
	if cf := w.renderCustomFields(issue); cf != "" {
		fmt.Fprintf(&b, "\n**自定义:** %s", cf)
	}
	return b.String()
}

// renderChangeCardMD 生成变更明细 markdown, attrSet 非空时只输出指定属性。
func renderChangeCardMD(j Journal, attrSet map[string]bool, cfNames map[string]string) string {
	var b strings.Builder
	b.WriteString("**变更明细:**\n")
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
			fmt.Fprintf(&b, "- 属性 [%s]: %s → %s\n", name, fmtVal(d.OldValue), fmtVal(d.NewValue))
		case "cf":
			fmt.Fprintf(&b, "- 自定义属性 [%s]: %s → %s\n", name, fmtVal(d.OldValue), fmtVal(d.NewValue))
		case "attachment":
			fmt.Fprintf(&b, "- 附件 [%s] 添加\n", name)
		default:
			fmt.Fprintf(&b, "- 变更: %s → %s\n", fmtVal(d.OldValue), fmtVal(d.NewValue))
		}
	}
	if notes := strings.TrimSpace(j.Notes); notes != "" {
		oneLine := strings.NewReplacer("\r\n", " ", "\n", " ").Replace(notes)
		b.WriteString("- 备注: " + oneLine)
	}
	return b.String()
}

type batchGetIDResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		UserList []struct {
			UserID string `json:"user_id"`
		} `json:"user_list"`
	} `json:"data"`
}

// GetOpenIDByMobile 通过手机号查询 open_id (需要 contact:user.base:readonly 权限)。
func (f *FeishuClient) GetOpenIDByMobile(mobile string) (string, error) {
	return f.lookupOpenID(map[string][]string{"mobiles": {mobile}})
}

// GetOpenIDByEmail 通过邮箱查询 open_id (需要 contact:user.base:readonly 权限)。
func (f *FeishuClient) GetOpenIDByEmail(email string) (string, error) {
	return f.lookupOpenID(map[string][]string{"emails": {email}})
}

func (f *FeishuClient) lookupOpenID(body map[string][]string) (string, error) {
	token, err := f.getToken()
	if err != nil {
		return "", err
	}
	payload, _ := json.Marshal(body)
	u := f.apiBase + "/open-apis/contact/v3/users/batch_get_id?user_id_type=open_id"
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("查询 open_id 失败: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	var br batchGetIDResponse
	if err := json.Unmarshal(data, &br); err != nil {
		return "", fmt.Errorf("解析查询响应失败: %s", truncate(string(data), 300))
	}
	if br.Code != 0 {
		return "", fmt.Errorf("查询 open_id 失败: code=%d msg=%s", br.Code, br.Msg)
	}
	if len(br.Data.UserList) == 0 {
		return "", fmt.Errorf("未查询到该手机号/邮箱对应的用户, 请检查是否在应用的可用范围内")
	}
	return br.Data.UserList[0].UserID, nil
}
