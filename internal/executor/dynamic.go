// 三期创建通道的集群客户端（R2.2）：dynamic client + discovery/RESTMapper。
//
// 依赖方向：executor 只接受 internal/kubernetes.NewRESTConfig 产出的 rest.Config，
// 不反向依赖只读门面；工具层通过 MappingFor 拿到 GVR/scope，不需要自己 import client-go。
package executor

import (
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	discmem "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
)

// writeChannel 是创建通道用到的一对集群客户端。
type writeChannel struct {
	dyn    dynamic.Interface
	mapper meta.RESTMapper
}

// channel 惰性装配写通道：优先用注入的客户端（测试），否则按 rest.Config 构造。
func (r *Runner) channel() (*writeChannel, error) {
	r.channelOnce.Do(func() {
		switch {
		case r.dyn != nil && r.mapper != nil:
			r.channelValue = &writeChannel{dyn: r.dyn, mapper: r.mapper}
		case r.dyn != nil || r.mapper != nil:
			r.channelErr = errors.New("写通道装配不完整：Dynamic 与 Mapper 需同时注入")
		case r.restConfig == nil:
			r.channelErr = errors.New("写通道未装配：缺少集群连接配置（确认 kubeconfig 可用且 agent.create.enabled=true）")
		default:
			r.channelValue, r.channelErr = newWriteChannel(r.restConfig)
		}
	})
	if r.channelErr != nil {
		return nil, r.channelErr
	}
	return r.channelValue, nil
}

// newWriteChannel 用 discovery 缓存构造 dynamic client 与 RESTMapper。
func newWriteChannel(rc *rest.Config) (*writeChannel, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("构造 discovery 客户端: %w", err)
	}
	dyn, err := dynamic.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("构造 dynamic 客户端: %w", err)
	}
	return &writeChannel{dyn: dyn, mapper: restmapper.NewShortcutExpander(
		restmapper.NewDeferredDiscoveryRESTMapper(discmem.NewMemCacheClient(dc)), dc, nil,
	)}, nil
}

// resource 返回对象对应的动态客户端（cluster-scoped 忽略 namespace）。
func (r *Runner) resource(gvr schema.GroupVersionResource, namespace string, namespaced bool) (dynamic.ResourceInterface, error) {
	ch, err := r.channel()
	if err != nil {
		return nil, err
	}
	if namespaced {
		if strings.TrimSpace(namespace) == "" {
			return nil, errors.New("namespaced 资源缺少 namespace")
		}
		return ch.dyn.Resource(gvr).Namespace(namespace), nil
	}
	return ch.dyn.Resource(gvr), nil
}

// ParseGVK 解析 apiVersion + kind（"v1" → core 组，"apps/v1" → apps 组）。
func ParseGVK(apiVersion, kind string) schema.GroupVersionKind {
	gvk := schema.GroupVersionKind{Kind: strings.TrimSpace(kind)}
	gv := strings.SplitN(strings.TrimSpace(apiVersion), "/", 2)
	if len(gv) == 2 {
		gvk.Group, gvk.Version = strings.TrimSpace(gv[0]), strings.TrimSpace(gv[1])
	} else {
		gvk.Version = strings.TrimSpace(apiVersion)
	}
	return gvk
}
