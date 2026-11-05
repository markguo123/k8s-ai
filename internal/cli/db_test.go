package cli

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/config"
)

var dbMigratePattern = regexp.MustCompile(`schema_version=(\d+)->(\d+) \(expected (\d+)\)`)

func parseDBMigrate(t *testing.T, out string) (before, after, expected int) {
	t.Helper()
	m := dbMigratePattern.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("db migrate 输出缺少版本信息: %q", out)
	}
	for i, dst := range []*int{&before, &after, &expected} {
		v, err := strconv.Atoi(m[i+1])
		if err != nil {
			t.Fatalf("解析版本失败: %v", err)
		}
		*dst = v
	}
	return before, after, expected
}

// TestDBMigrateIdempotent 锁 P6.5 迁移入口：首次把空库升到当前版本，重复执行是 no-op，
// 因此可以安全地作为升级 Job / initContainer 反复跑。
func TestDBMigrateIdempotent(t *testing.T) {
	cfg := defaultCfg(t)
	dbPath := filepath.Join(t.TempDir(), "data", "platform.db")
	cfg.Platform.Store.SQLitePath = dbPath

	root := NewRootCmd(testDeps(cfg, &fakeScan{}))
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"db", "migrate"})
	if err := root.Execute(); err != nil {
		t.Fatalf("首次迁移失败: %v (%s)", err, buf.String())
	}
	before, after, expected := parseDBMigrate(t, buf.String())
	if before != 0 || after != expected || expected == 0 {
		t.Fatalf("空库应从零升到当前版本, got %d->%d expected %d", before, after, expected)
	}
	if !strings.Contains(buf.String(), "driver=sqlite") {
		t.Fatalf("应回报驱动与安全目标, got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "sqlite:") {
		t.Fatalf("应回报 sqlite 目标路径, got %q", buf.String())
	}

	// 第二次：no-op，版本不变。
	root2 := NewRootCmd(testDeps(cfg, &fakeScan{}))
	var buf2 bytes.Buffer
	root2.SetOut(&buf2)
	root2.SetErr(&buf2)
	root2.SetArgs([]string{"db", "migrate"})
	if err := root2.Execute(); err != nil {
		t.Fatalf("重复迁移应幂等: %v (%s)", err, buf2.String())
	}
	before2, after2, expected2 := parseDBMigrate(t, buf2.String())
	if before2 != after || after2 != expected2 {
		t.Fatalf("重复执行不应改动 schema, got %d->%d expected %d", before2, after2, expected2)
	}
}

// TestDBMigrateDBFlagOverridesPath 锁 --db 只对 sqlite 生效（清单与手工运维都要用）。
func TestDBMigrateDBFlagOverridesPath(t *testing.T) {
	cfg := defaultCfg(t)
	cfg.Platform.Store.SQLitePath = filepath.Join(t.TempDir(), "unused.db")
	target := filepath.Join(t.TempDir(), "flagged.db")

	root := NewRootCmd(testDeps(cfg, &fakeScan{}))
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"db", "migrate", "--db", target})
	if err := root.Execute(); err != nil {
		t.Fatalf("migrate --db: %v", err)
	}
	if !strings.Contains(buf.String(), target) {
		t.Fatalf("应迁移到 --db 指定路径, got %q", buf.String())
	}
}

// TestMySQLTargetRedacted 锁 MySQL 主路径：DSN 里的口令绝不进日志/命令输出
// （目标摘要只含 user@net(addr)/db）。
func TestMySQLTargetRedacted(t *testing.T) {
	got := mysqlTarget("appuser:s3cr3t-pass@tcp(mysql.internal:3306)/k8s_ai?charset=utf8mb4&parseTime=true")
	if want := "mysql:appuser@tcp(mysql.internal:3306)/k8s_ai"; got != want {
		t.Fatalf("目标摘要异常: got %q want %q", got, want)
	}
	if strings.Contains(got, "s3cr3t-pass") {
		t.Fatal("目标摘要不得包含口令")
	}
}

// TestOpenPlatformStoreMySQLFailsFast 锁启动即失败：MySQL 主路径连不上时
// openPlatformStore 直接报错（错误里带脱敏目标），不允许带着坏存储起来接活。
func TestOpenPlatformStoreMySQLFailsFast(t *testing.T) {
	cfg, err := config.Load(config.LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Platform.Store.Driver = config.StoreDriverMySQL
	cfg.Platform.Store.DSN = "appuser:s3cr3t-pass@tcp(mysql.internal:3306)/k8s_ai?charset=utf8mb4&parseTime=true"
	_, _, err = openPlatformStore(cfg)
	if err == nil {
		t.Fatal("连不上 MySQL 时应启动失败")
	}
	if strings.Contains(err.Error(), "s3cr3t-pass") {
		t.Fatalf("错误文本不得含口令: %v", err)
	}
	if !strings.Contains(err.Error(), "k8s_ai") {
		t.Fatalf("错误应指名目标库便于排查: %v", err)
	}
}

// TestOpenPlatformStoreGuards 锁两个装配护栏：未加载配置、空 sqlite 路径。
func TestOpenPlatformStoreGuards(t *testing.T) {
	if _, _, err := openPlatformStore(nil); err == nil {
		t.Fatal("未加载配置应报错")
	}
	cfg := defaultCfg(t)
	cfg.Platform.Store.SQLitePath = ""
	if _, _, err := openPlatformStore(cfg); err == nil {
		t.Fatal("sqlite 路径为空应报错")
	}
}
