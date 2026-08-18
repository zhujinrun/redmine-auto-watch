# redmine-auto-watch

监控 Redmine 任务属性变化，任务有变化时打印任务详情到控制台/日志，并通过飞书机器人私发富文本卡片提醒。

## 功能特性

- 轮询 Redmine REST API，检测任务的属性变化（状态、指派给、优先级、完成度等）
- 属性变化/新增任务时输出完整任务详情
- 飞书通知：自建应用私发交互式富文本卡片，卡片带「打开任务」按钮
- 两种运行方式：
  - 前台运行：`go run .` 或双击编译后的 exe
  - Windows 服务：后台运行、开机自启
- 状态持久化：记录每个任务已处理的 journal，避免重复通知
- token 自动缓存与刷新，token 失效自动重试

## 环境要求

- Go 1.23+（本项目在 go1.26.6 下开发验证）
- Windows（服务功能依赖 Windows 服务管理器）

## 构建

```powershell
go build -o redmine-watch.exe .
```

## 配置

程序从 **exe 所在目录**（`go run` 时取当前目录）读取 `config.json`：

```json
{
  "redmine_url": "https://redmine.aiclick.net",
  "api_key": "你的 Redmine API Key",
  "poll_interval": 30,
  "log_file": "watch.log",
  "state_file": "state.json",
  "timezone": "Asia/Shanghai",
  "feishu": {
    "app_id": "飞书自建应用 App ID",
    "app_secret": "飞书自建应用 App Secret",
    "api_base": "https://open.feishu.cn",
    "receive_id_type": "open_id",
    "receive_id": "接收人的 open_id"
  }
}
```

| 字段 | 说明 |
| --- | --- |
| `redmine_url` | Redmine 服务地址 |
| `api_key` | Redmine API Key（个人设置 → 访问密钥） |
| `poll_interval` | 轮询间隔（秒） |
| `log_file` / `state_file` | 日志文件、状态文件（生成在 exe 同目录） |
| `timezone` | 打印时间使用的时区 |
| `feishu.app_id` / `app_secret` | 飞书自建应用凭证（不填则跳过飞书通知） |
| `feishu.api_base` | 飞书开放平台地址（国内版默认，国际版填 `https://open.larksuite.com`） |
| `feishu.receive_id_type` | 接收人标识类型：`open_id`（默认）/ `user_id` / `email` / `mobile` |
| `feishu.receive_id` | 接收人的标识 |

## 运行方式

### 前台运行

```powershell
go run .
# 或
.\redmine-watch.exe run
# 或直接双击 redmine-watch.exe
```

前台模式下，任务详情输出到控制台，同时写入日志文件。启动报错时会提示「按回车键退出」，避免窗口一闪而过。

### 安装为 Windows 服务（后台运行）

以**管理员身份**执行：

```powershell
.\redmine-watch.exe install   # 安装并设为开机自启
.\redmine-watch.exe start     # 启动服务
.\redmine-watch.exe stop      # 停止服务
.\redmine-watch.exe status    # 查看服务状态
.\redmine-watch.exe uninstall # 卸载服务
```

服务模式下只写日志文件，不占用控制台。

## 飞书通知配置

1. 在 [飞书开放平台](https://open.feishu.cn) 创建**企业自建应用**，获取 App ID / App Secret
2. 在「权限管理」开通：
   - `im:message`：发送单聊/群聊消息（必需）
   - `contact:user.id:readonly`：通过手机号/邮箱查询 open_id（查询接收人时需要）
3. 发布应用版本，并将接收人加入应用**可用范围**
4. 查询接收人的 open_id（app_id/app_secret 自动读取 config.json）：

```powershell
.\redmine-watch.exe openid --mobile 13800000000
# 或
.\redmine-watch.exe openid --email user@company.com
```

5. 将查询到的 open_id 填入 `config.json` 的 `feishu.receive_id`

## 日志与状态

- `watch.log`：UTF-8 编码日志（服务/前台均写入）
- `state.json`：记录 `last_updated` 与每个任务的已处理 journal id。首次运行从启动时刻开始监控；如需追溯历史，可手动把 `last_updated` 改成更早的时间

## 测试

```powershell
go test ./...
```

包含对变化检测逻辑（新增/变化/无变化）、飞书 token 缓存与发送重试的单元测试。
