package objectstore

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// driver=off 必须对每个动作都表态。它若对读返回 ErrNotFound、对删返回 nil，就是对着
// 调用方撒谎：前者会被下载路径标成 missing（其实从未启用），后者会让业务行被删掉而
// 内容还在别的后端里躺着。
func TestDisabledBackendRefusesEverything(t *testing.T) {
	st := NewDisabled()
	if st.Driver() != DriverOff {
		t.Fatalf("Driver = %q, want %q", st.Driver(), DriverOff)
	}
	ctx := context.Background()
	key := "k8sai/reports/c/" + strings.Repeat("a", 64)

	if _, err := st.Put(ctx, Spec{Category: CategoryReports, Scope: []string{"c"}}, strings.NewReader("x")); !errors.Is(err, ErrDisabled) {
		t.Errorf("Put = %v, want ErrDisabled", err)
	}
	if _, _, err := st.Get(ctx, key); !errors.Is(err, ErrDisabled) {
		t.Errorf("Get = %v, want ErrDisabled", err)
	}
	if _, err := st.Stat(ctx, key); !errors.Is(err, ErrDisabled) {
		t.Errorf("Stat = %v, want ErrDisabled", err)
	}
	if err := st.Delete(ctx, key); !errors.Is(err, ErrDisabled) {
		t.Errorf("Delete = %v, want ErrDisabled", err)
	}
	if err := st.List(ctx, "k8sai", func(Ref) error { return nil }); !errors.Is(err, ErrDisabled) {
		t.Errorf("List = %v, want ErrDisabled", err)
	}
	if err := st.Close(); err != nil {
		t.Errorf("Close = %v, want nil", err)
	}

	maintainer, ok := st.(Maintainer)
	if !ok {
		t.Fatal("disabled backend must implement Maintainer")
	}
	if err := maintainer.Ensure(ctx); !errors.Is(err, ErrDisabled) {
		t.Errorf("Ensure = %v, want ErrDisabled", err)
	}
	probe, ok := st.(Probe)
	if !ok {
		t.Fatal("disabled backend must implement Probe")
	}
	if err := probe.Probe(ctx); !errors.Is(err, ErrDisabled) {
		t.Errorf("Probe = %v, want ErrDisabled", err)
	}

	// 未启用不是故障：装配层靠 DisabledError 的 Is 把它与真故障分开，同时还能带出操作名。
	var disabled *DisabledError
	_, err := st.Put(ctx, Spec{Category: CategoryExports, Scope: []string{"c"}}, strings.NewReader("x"))
	if !errors.As(err, &disabled) || !strings.Contains(disabled.Op, CategoryExports) {
		t.Fatalf("Put error should carry the refused operation, got %v", err)
	}
}

// off 后端刻意不签发预签名 URL：库里/关闭态都没有可对外签发的东西。
func TestDisabledBackendIsNotAPresigner(t *testing.T) {
	if _, ok := NewDisabled().(Presigner); ok {
		t.Fatal("driver=off must not implement Presigner")
	}
}
