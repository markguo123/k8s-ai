package scanner

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestNormalizePodPVCRefs 验证 Pod 归一化会提取声明的 PVC。
func TestNormalizePodPVCRefs(t *testing.T) {
	p := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-0", Namespace: "prod"},
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{
				{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "web-data"}}},
				{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
		},
	}
	info := normalizePod(p)
	if len(info.PVCRefs) != 1 || info.PVCRefs[0].Name != "web-data" || info.PVCRefs[0].Namespace != "prod" {
		t.Fatalf("PVCRefs = %+v, want [web-data]", info.PVCRefs)
	}
}

// TestNormalizeWorkloadOwnerRefs 验证 workload 归一化会保留 ownerReferences。
func TestNormalizeWorkloadOwnerRefs(t *testing.T) {
	rs := appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-rs",
			Namespace: "prod",
			OwnerReferences: []metav1.OwnerReference{{
				Kind: "Deployment",
				Name: "web-dep",
				UID:  "dep-uid",
			}},
		},
	}
	info := normalizeReplicaSet(rs)
	if len(info.OwnerRefs) != 1 || info.OwnerRefs[0].Name != "web-dep" || info.OwnerRefs[0].Kind != "Deployment" {
		t.Fatalf("OwnerRefs = %+v", info.OwnerRefs)
	}
}

// TestNormalizeStorageLinkFields 验证 PVC/PV 保留关联字段。
func TestNormalizeStorageLinkFields(t *testing.T) {
	scName := "fast"
	pvc := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "web-data", Namespace: "prod"},
		Spec: corev1.PersistentVolumeClaimSpec{
			VolumeName:       "pv-1",
			StorageClassName: &scName,
		},
	}
	info := normalizePVC(pvc)
	if info.VolumeName != "pv-1" || info.StorageClassName != "fast" {
		t.Fatalf("PVC 关联字段 = %+v", info)
	}
	pv := corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-1"},
		Spec:       corev1.PersistentVolumeSpec{StorageClassName: "fast"},
	}
	pvInfo := normalizePV(pv)
	if pvInfo.StorageClassName != "fast" {
		t.Fatalf("PV StorageClassName = %q", pvInfo.StorageClassName)
	}
}

// TestNormalizeEndpointSliceLink 验证 EndpointSlice 保留 Service 与目标 Pod。
func TestNormalizeEndpointSliceLink(t *testing.T) {
	es := discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-svc-abc",
			Namespace: "prod",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "web-svc"},
		},
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses:  []string{"10.0.0.1"},
				Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(true)},
				TargetRef:  &corev1.ObjectReference{Kind: "Pod", Namespace: "prod", Name: "web-0", UID: "u1"},
			},
			{
				Addresses:  []string{"10.0.0.2"},
				Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(false)},
				TargetRef:  &corev1.ObjectReference{Kind: "Pod", Namespace: "prod", Name: "web-0", UID: "u1"},
			},
		},
	}
	info := normalizeEndpointSlice(es)
	if info.ServiceName != "web-svc" {
		t.Fatalf("ServiceName = %q", info.ServiceName)
	}
	if len(info.TargetPods) != 1 || info.TargetPods[0].Name != "web-0" {
		t.Fatalf("TargetPods = %+v, want 去重后的 web-0", info.TargetPods)
	}
	if info.Ready != 1 || info.NotReady != 1 {
		t.Fatalf("Ready/NotReady = %d/%d", info.Ready, info.NotReady)
	}
}

func boolPtr(b bool) *bool {
	return &b
}
