package main

import (
	"os"
	"strings"
	"testing"
)

// 在 go test / go run 场景下, os.Executable() 指向临时编译目录,
// baseDir() 必须回退到当前工作目录。
func TestBaseDirFallsBackToCwd(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("executable: %s", exe)
	temp := strings.ToLower(os.TempDir())
	lower := strings.ToLower(exe)
	if !strings.Contains(lower, "go-build") && !strings.Contains(lower, temp) {
		t.Skip("当前 exe 不在临时编译目录, 跳过")
	}
	cwd, _ := os.Getwd()
	if got := baseDir(); got != cwd {
		t.Fatalf("baseDir() = %q, 期望当前目录 %q", got, cwd)
	}
}
