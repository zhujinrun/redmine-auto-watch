# redmine-auto-watch

监控 Redmine 任务属性变化，任务有变化时打印任务详情到控制台/日志。

## 功能特性

- 轮询 Redmine REST API，检测任务的属性变化（状态、指派给、优先级、完成度等）
- 属性变化/新增任务时输出完整任务详情
- 两种运行方式：
  - 前台运行：`go run .` 或双击编译后的 exe
  - Windows 服务：后台运行、开机自启
- 状态持久化：记录每个任务已处理的 journal，避免重复通知

## 环境要求

- Go 1.23+（本项目在 go1.26.6 下开发验证）
- Windows（服务功能依赖 Windows 服务管理器）

## 构建

```powershell
go build -o redmine-watch.exe .
```

## 配置

复制 `config.example.json` 为 `config.json`（已加入 .gitignore，不会入库），程序从 **exe 所在目录**（`go run` 时取当前目录）读取：

```json
{
  "redmine_url": "https://redmine.aiclick.net",
  "api_key": "你的 Redmine API Key",
  "poll_interval": 30,
  "log_file": "watch.log",
  "state_file": "state.json",
  "timezone": "Asia/Shanghai"
}
```

| 字段 | 说明 |
| --- | --- |
| `redmine_url` | Redmine 服务地址 |
| `api_key` | Redmine API Key（个人设置 → 访问密钥） |
| `poll_interval` | 轮询间隔（秒） |
| `log_file` / `state_file` | 日志文件、状态文件（生成在 exe 同目录） |
| `timezone` | 打印时间使用的时区 |

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

## 日志与状态

- `watch.log`：UTF-8 编码日志（服务/前台均写入）
- `state.json`：记录 `last_updated` 与每个任务的已处理 journal id。首次运行从启动时刻开始监控；如需追溯历史，可手动把 `last_updated` 改成更早的时间

## 测试

```powershell
go test ./...
```

包含对变化检测逻辑（新增/变化/无变化）的单元测试。
