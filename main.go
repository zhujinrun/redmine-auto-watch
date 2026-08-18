package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "time/tzdata"

	"github.com/kardianos/service"
	"golang.org/x/sys/windows"
)

func enableUTF8Console() {
	_ = windows.SetConsoleOutputCP(65001)
	_ = windows.SetConsoleCP(65001)
}

var svcConfig = &service.Config{
	Name:        "RedmineAutoWatch",
	DisplayName: "Redmine Auto Watch",
	Description: "监控 Redmine 任务属性变化并打印任务详情到日志",
}

// baseDir 是配置/状态/日志文件的存放目录。
// 优先级: 环境变量 REDMINE_WATCH_DIR > 前台运行时的当前目录 > exe 所在目录。
// 其中 go run 编译出的临时 exe 位于 %TEMP%\go-build... , 需要用当前目录兜底,
// 否则会找不到 config.json。
func baseDir() string {
	if d := os.Getenv("REDMINE_WATCH_DIR"); d != "" {
		return d
	}
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	dir := filepath.Dir(exe)
	temp := strings.ToLower(os.TempDir())
	lower := strings.ToLower(dir)
	if strings.Contains(lower, "go-build") || strings.Contains(lower, temp) {
		if cwd, err := os.Getwd(); err == nil {
			return cwd
		}
	}
	return dir
}

func runForeground(baseDir string) error {
	enableUTF8Console()
	cfg, err := loadConfig(baseDir)
	if err != nil {
		return fmt.Errorf("配置错误: %w", err)
	}
	logger, err := setupLogger(baseDir, cfg.LogFile, true)
	if err != nil {
		return fmt.Errorf("无法创建日志文件: %w", err)
	}
	logger.Printf("Redmine 监控已启动: %s (轮询间隔 %d 秒)", cfg.RedmineURL, cfg.PollInterval)
	logger.Printf("前台运行模式, 日志文件: %s (Ctrl+C 退出)", cfg.LogFile)
	w := newWatcher(cfg, baseDir, logger)
	w.processOnce()
	ticker := time.NewTicker(time.Duration(cfg.PollInterval) * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		w.processOnce()
	}
	return nil
}

type program struct {
	baseDir string
}

func (p *program) Start(s service.Service) error {
	cfg, err := loadConfig(p.baseDir)
	if err != nil {
		return err
	}
	go runAsService(cfg, p.baseDir)
	return nil
}

func (p *program) Stop(s service.Service) error {
	return nil
}

func runAsService(cfg Config, baseDir string) {
	logger, err := setupLogger(baseDir, cfg.LogFile, false)
	if err != nil {
		log.Printf("无法创建日志文件: %v", err)
		return
	}
	logger.Printf("Redmine 监控已启动(Windows 服务): %s (轮询间隔 %d 秒)", cfg.RedmineURL, cfg.PollInterval)
	logger.Printf("日志文件: %s", cfg.LogFile)
	w := newWatcher(cfg, baseDir, logger)
	w.processOnce()
	ticker := time.NewTicker(time.Duration(cfg.PollInterval) * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		w.processOnce()
	}
}

func printUsage() {
	fmt.Println("redmine-watch - 监控 Redmine 任务属性变化并打印任务详情")
	fmt.Println()
	fmt.Println("运行模式:")
	fmt.Println("  直接运行(无参数 / run): 前台运行, 输出到控制台+日志文件")
	fmt.Println("  install / start / stop / uninstall / status: 管理 Windows 服务(后台运行)")
	fmt.Println()
	fmt.Println("用法: redmine-watch.exe [run|install|start|stop|uninstall|status]")
	fmt.Println("  run        前台运行(go run . 或双击 exe 效果相同)")
	fmt.Println("  install    安装为 Windows 服务(需管理员权限, 开机自启)")
	fmt.Println("  start      启动服务")
	fmt.Println("  stop       停止服务")
	fmt.Println("  uninstall  卸载服务")
	fmt.Println("  status     查看服务状态")
}

func main() {
	base := baseDir()

	// 无参数且不是被服务管理器启动时(即双击/命令行直接运行) → 前台运行
	if len(os.Args) == 1 {
		if service.Interactive() {
			if err := runForeground(base); err != nil {
				fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
				fmt.Println("按回车键退出...")
				_, _ = fmt.Scanln()
				os.Exit(1)
			}
			return
		}
		// 由 Windows 服务管理器启动
		svc, err := service.New(&program{baseDir: base}, svcConfig)
		if err != nil {
			log.Fatalf("创建服务失败: %v", err)
		}
		if err := svc.Run(); err != nil {
			log.Fatalf("运行服务失败: %v", err)
		}
		return
	}

	switch os.Args[1] {
	case "run":
		if err := runForeground(base); err != nil {
			fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
			fmt.Println("按回车键退出...")
			_, _ = fmt.Scanln()
			os.Exit(1)
		}
	case "install", "uninstall", "start", "stop", "status":
		svc, err := service.New(&program{baseDir: base}, svcConfig)
		if err != nil {
			log.Fatalf("创建服务失败: %v", err)
		}
		if err := manageService(svc, os.Args[1]); err != nil {
			log.Fatalf("%v", err)
		}
	default:
		printUsage()
	}
}

func manageService(svc service.Service, cmd string) error {
	switch cmd {
	case "install":
		if err := svc.Install(); err != nil {
			return fmt.Errorf("安装服务失败: %w (请以管理员身份运行)", err)
		}
		fmt.Println("服务已安装: RedmineAutoWatch (开机自启)")
	case "uninstall":
		if err := svc.Uninstall(); err != nil {
			return fmt.Errorf("卸载服务失败: %w", err)
		}
		fmt.Println("服务已卸载")
	case "start":
		if err := svc.Start(); err != nil {
			return fmt.Errorf("启动服务失败: %w", err)
		}
		fmt.Println("服务已启动")
	case "stop":
		if err := svc.Stop(); err != nil {
			return fmt.Errorf("停止服务失败: %w", err)
		}
		fmt.Println("服务已停止")
	case "status":
		status, err := svc.Status()
		if err != nil {
			return fmt.Errorf("获取服务状态失败: %w", err)
		}
		fmt.Printf("服务状态: %v\n", status)
	}
	return nil
}
