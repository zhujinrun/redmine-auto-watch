package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestBuildCard(t *testing.T) {
	card := buildCard("[属性变化] #1 测试", "**项目:** 技术部", "时间: 2026-08-18", "https://redmine.aiclick.net/issues/1", "blue")
	header := card["header"].(map[string]interface{})
	if header["template"] != "blue" {
		t.Fatalf("template 应为 blue, 实际 %v", header["template"])
	}
	title := header["title"].(map[string]interface{})
	if title["content"] != "[属性变化] #1 测试" {
		t.Fatalf("title 内容不正确: %v", title["content"])
	}
	elements := card["elements"].([]map[string]interface{})
	if len(elements) != 3 {
		t.Fatalf("elements 数量应为 3 (正文+按钮+note), 实际 %d", len(elements))
	}
	// 最后一个元素应为 note
	note := elements[len(elements)-1]
	if note["tag"] != "note" {
		t.Fatalf("最后一个元素应为 note, 实际 %v", note["tag"])
	}
}

type feishuMock struct {
	mu            sync.Mutex
	tokenCalls    int
	msgCalls      int
	tokenCode     int
	msgCodes      []int
	receiveIDType string
	received      []map[string]string
}

func newFeishuMock(t *testing.T, codeOnMsg []int) (*httptest.Server, *feishuMock) {
	m := &feishuMock{msgCodes: codeOnMsg}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(r.URL.Path, "tenant_access_token"):
			m.mu.Lock()
			m.tokenCalls++
			m.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","tenant_access_token":"t-token","expire":7200}`))
		case strings.Contains(r.URL.Path, "im/v1/messages"):
			m.mu.Lock()
			idx := m.msgCalls
			m.msgCalls++
			var code int
			if idx < len(m.msgCodes) {
				code = m.msgCodes[idx]
			}
			m.mu.Unlock()
			var payload map[string]string
			_ = json.Unmarshal(body, &payload)
			if m.receiveIDType == "" {
				m.receiveIDType = r.URL.Query().Get("receive_id_type")
			}
			m.received = append(m.received, payload)
			w.Header().Set("Content-Type", "application/json")
			if code != 0 {
				_, _ = w.Write([]byte(fmt.Sprintf(`{"code":%d,"msg":"mock error"}`, code)))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	return srv, m
}

func TestSendCardAndTokenCache(t *testing.T) {
	srv, m := newFeishuMock(t, nil)
	defer srv.Close()

	client := newFeishuClient(FeishuConfig{
		APIBase:   srv.URL,
		AppID:     "app",
		AppSecret: "secret",
		ReceiveID: "ou_123",
	})
	card := buildCard("标题", "正文", "", "", "blue")
	if err := client.SendCard(card); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if err := client.SendCard(card); err != nil {
		t.Fatalf("第二次发送失败: %v", err)
	}
	if m.tokenCalls != 1 {
		t.Fatalf("token 应只请求一次(带缓存), 实际 %d", m.tokenCalls)
	}
	if m.msgCalls != 2 {
		t.Fatalf("消息应发送 2 次, 实际 %d", m.msgCalls)
	}
	if m.receiveIDType != "open_id" {
		t.Fatalf("默认 receive_id_type 应为 open_id, 实际 %s", m.receiveIDType)
	}
	if len(m.received) == 0 || m.received[0]["receive_id"] != "ou_123" {
		t.Fatalf("receive_id 不正确: %v", m.received)
	}
	if len(m.received) == 0 || m.received[0]["msg_type"] != "interactive" {
		t.Fatalf("msg_type 应为 interactive, 实际 %v", m.received)
	}
}

func TestSendCardRetryOnTokenExpired(t *testing.T) {
	// 第一次消息发送返回 token 失效(99991663), 应刷新 token 重试并成功
	srv, m := newFeishuMock(t, []int{99991663, 0})
	defer srv.Close()

	client := newFeishuClient(FeishuConfig{
		APIBase:   srv.URL,
		AppID:     "app",
		AppSecret: "secret",
		ReceiveID: "ou_123",
	})
	card := buildCard("标题", "正文", "", "", "blue")
	if err := client.SendCard(card); err != nil {
		t.Fatalf("应自动重试成功, 实际失败: %v", err)
	}
	if m.msgCalls != 2 {
		t.Fatalf("消息应尝试 2 次, 实际 %d", m.msgCalls)
	}
	if m.tokenCalls != 2 {
		t.Fatalf("token 应请求 2 次(刷新), 实际 %d", m.tokenCalls)
	}
}

func TestSendCardPermanentError(t *testing.T) {
	// 业务错误(如 19001 无权限)不重试, 直接报错
	srv, m := newFeishuMock(t, []int{19001})
	defer srv.Close()

	client := newFeishuClient(FeishuConfig{
		APIBase:   srv.URL,
		AppID:     "app",
		AppSecret: "secret",
		ReceiveID: "ou_123",
	})
	card := buildCard("标题", "正文", "", "", "blue")
	if err := client.SendCard(card); err == nil {
		t.Fatal("应返回错误")
	} else if !strings.Contains(err.Error(), "19001") {
		t.Fatalf("错误信息应包含 code 19001, 实际: %v", err)
	}
	if m.msgCalls != 1 {
		t.Fatalf("业务错误不应重试, 实际发送 %d 次", m.msgCalls)
	}
}
