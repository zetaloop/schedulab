package schedulab

import (
	"context"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fwk "k8s.io/kube-scheduler/framework"
	schedulerframework "k8s.io/kubernetes/pkg/scheduler/framework"
)

func TestLessOrdersByPriorityAttemptsAndTimestamp(t *testing.T) {
	plugin := &Plugin{}
	now := time.Now()
	low := queuedPodInfo(t, newPod("low", "10m", "16Mi"), 0, now)
	high := queuedPodInfo(t, newPod("high", "10m", "16Mi", withPriority(100)), 0, now)

	if !plugin.Less(high, low) {
		t.Fatal("higher priority pod should be ordered first")
	}

	earlier := queuedPodInfo(t, newPod("earlier", "10m", "16Mi"), 1, now.Add(-time.Second))
	later := queuedPodInfo(t, newPod("later", "10m", "16Mi"), 1, now)
	if !plugin.Less(earlier, later) {
		t.Fatal("older pod should be ordered first when priority and attempts match")
	}

	moreAttempts := queuedPodInfo(t, newPod("more-attempts", "10m", "16Mi"), 2, now)
	fewerAttempts := queuedPodInfo(t, newPod("fewer-attempts", "10m", "16Mi"), 1, now.Add(-time.Hour))
	if !plugin.Less(moreAttempts, fewerAttempts) {
		t.Fatal("pod with more attempts should be ordered first when priority matches")
	}
}

func TestFilterRejectsNodeAboveUtilizationLimit(t *testing.T) {
	args := defaultArgs()
	args.normalize()
	plugin := &Plugin{args: args}
	running := newPod("running", "850m", "128Mi")
	incoming := newPod("incoming", "100m", "64Mi")
	nodeInfo := newNodeInfo("node-a", "1000m", "1Gi", running)

	status := plugin.Filter(context.Background(), nil, incoming, nodeInfo)
	if status == nil || status.Code() != fwk.Unschedulable {
		t.Fatalf("expected unschedulable status, got %v", status)
	}
}

func TestScorePrefersBalancedNode(t *testing.T) {
	args := defaultArgs()
	args.normalize()
	plugin := &Plugin{args: args}
	incoming := newPod("incoming", "100m", "100Mi")
	imbalanced := newNodeInfo("imbalanced", "1000m", "1000Mi", newPod("cpu-light-memory-heavy", "100m", "900Mi"))
	balanced := newNodeInfo("balanced", "1000m", "1000Mi", newPod("balanced-load", "400m", "400Mi"))

	imbalancedScore, status := plugin.Score(context.Background(), nil, incoming, imbalanced)
	if status != nil {
		t.Fatalf("unexpected status for imbalanced node: %v", status)
	}
	balancedScore, status := plugin.Score(context.Background(), nil, incoming, balanced)
	if status != nil {
		t.Fatalf("unexpected status for balanced node: %v", status)
	}
	if balancedScore <= imbalancedScore {
		t.Fatalf("expected balanced node score > imbalanced node score, got %d <= %d", balancedScore, imbalancedScore)
	}
}

func TestNormalizeSetsModeDefaults(t *testing.T) {
	args := Args{Mode: "precision", BindPods: true}
	args.normalize()
	if args.MaxCPUUtilization != 80 || args.MaxMemoryUtilization != 80 {
		t.Fatalf("precision mode limits = %d/%d, want 80/80", args.MaxCPUUtilization, args.MaxMemoryUtilization)
	}
	if args.CPUWeight != 40 || args.MemoryWeight != 40 || args.SpreadWeight != 20 {
		t.Fatalf("precision mode weights = %d/%d/%d, want 40/40/20", args.CPUWeight, args.MemoryWeight, args.SpreadWeight)
	}

	args = Args{Mode: "unknown", BindPods: true}
	args.normalize()
	if args.Mode != "balanced" || args.MaxCPUUtilization != 90 || args.MaxMemoryUtilization != 90 {
		t.Fatalf("unknown mode normalized to %q with limits %d/%d, want balanced 90/90", args.Mode, args.MaxCPUUtilization, args.MaxMemoryUtilization)
	}
}

func queuedPodInfo(t *testing.T, pod *v1.Pod, attempts int, timestamp time.Time) fwk.QueuedPodInfo {
	t.Helper()
	podInfo, err := schedulerframework.NewPodInfo(pod)
	if err != nil {
		t.Fatal(err)
	}
	return &schedulerframework.QueuedPodInfo{
		PodInfo:   podInfo,
		Attempts:  attempts,
		Timestamp: timestamp,
	}
}

func newNodeInfo(name, cpu, memory string, pods ...*v1.Pod) fwk.NodeInfo {
	nodeInfo := schedulerframework.NewNodeInfo(pods...)
	nodeInfo.SetNode(&v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: v1.NodeStatus{
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse(cpu),
				v1.ResourceMemory: resource.MustParse(memory),
			},
		},
	})
	return nodeInfo
}

type podOption func(*v1.Pod)

func withPriority(priority int32) podOption {
	return func(pod *v1.Pod) {
		pod.Spec.Priority = &priority
	}
}

func newPod(name, cpu, memory string, options ...podOption) *v1.Pod {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      name,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "main",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse(cpu),
							v1.ResourceMemory: resource.MustParse(memory),
						},
					},
				},
			},
		},
	}
	for _, option := range options {
		option(pod)
	}
	return pod
}
