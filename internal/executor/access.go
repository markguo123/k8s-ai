package executor

import (
	"context"
	"fmt"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CanI 对当前 kubeconfig/ServiceAccount 执行 SelfSubjectAccessReview。
// 这是执行前的可解释性辅助，不是权限来源；真正授权仍由 API Server 决定。
func (r *Runner) CanI(ctx context.Context, verb, group, resource, namespace, name string) (bool, error) {
	if r.cs == nil {
		return false, fmt.Errorf("authorization client is not assembled")
	}
	review := &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Verb:      verb,
				Group:     group,
				Resource:  resource,
				Namespace: namespace,
				Name:      name,
			},
		},
	}
	resp, err := r.cs.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("self subject access review: %w", err)
	}
	return resp.Status.Allowed, nil
}
