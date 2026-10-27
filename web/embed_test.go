package web

import (
	"testing"
	"testing/fstest"
)

// TestOnlyPlaceholder 锁"干净检出"判定：占位文件不算产物，任何真实文件都算。
func TestOnlyPlaceholder(t *testing.T) {
	cases := []struct {
		name string
		fsys fstest.MapFS
		want bool
	}{
		{"空目录", fstest.MapFS{}, true},
		{"仅占位", fstest.MapFS{".gitkeep": {Data: []byte("")}}, true},
		{"有首页", fstest.MapFS{"index.html": {Data: []byte("<html/>")}}, false},
		{"有资源", fstest.MapFS{".gitkeep": {}, "assets/app.js": {Data: []byte("x")}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := onlyPlaceholder(tc.fsys); got != tc.want {
				t.Fatalf("onlyPlaceholder = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDist 锁嵌入视图可用（占位 dist 也必须能取到根目录）。
func TestDist(t *testing.T) {
	if Dist() == nil {
		t.Fatal("Dist() 不应为 nil")
	}
}
