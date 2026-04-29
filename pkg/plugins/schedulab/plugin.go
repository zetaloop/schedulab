package schedulab

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	componentresource "k8s.io/component-helpers/resource"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	fwk "k8s.io/kube-scheduler/framework"
	frameworkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
)

const (
	Name = "Schedulab"

	annotationMaxCPUUtilization    = "schedulab.dev/max-cpu-utilization"
	annotationMaxMemoryUtilization = "schedulab.dev/max-memory-utilization"
	annotationMinFreeMilliCPU      = "schedulab.dev/min-free-cpu-milli"
	annotationMinFreeMemory        = "schedulab.dev/min-free-memory-bytes"
	nodeDisabledLabel              = "schedulab.dev/disabled"
)

type Args struct {
	Mode                   string `json:"mode,omitempty"`
	BindPods               bool   `json:"bindPods,omitempty"`
	MaxCPUUtilization      int64  `json:"maxCPUUtilization,omitempty"`
	MaxMemoryUtilization   int64  `json:"maxMemoryUtilization,omitempty"`
	CPUWeight              int64  `json:"cpuWeight,omitempty"`
	MemoryWeight           int64  `json:"memoryWeight,omitempty"`
	SpreadWeight           int64  `json:"spreadWeight,omitempty"`
	PermitWaitSeconds      int64  `json:"permitWaitSeconds,omitempty"`
	PermitTimeoutSeconds   int64  `json:"permitTimeoutSeconds,omitempty"`
	MaxReservationsPerNode int64  `json:"maxReservationsPerNode,omitempty"`
}

type Plugin struct {
	args Args
	fh   fwk.Handle

	mu                 sync.Mutex
	reservedPodToNode  map[string]string
	reservationsByNode map[string]int64
}

var _ fwk.QueueSortPlugin = (*Plugin)(nil)
var _ fwk.FilterPlugin = (*Plugin)(nil)
var _ fwk.ScorePlugin = (*Plugin)(nil)
var _ fwk.ReservePlugin = (*Plugin)(nil)
var _ fwk.PermitPlugin = (*Plugin)(nil)
var _ fwk.BindPlugin = (*Plugin)(nil)
var _ fwk.EnqueueExtensions = (*Plugin)(nil)
var _ fwk.SignPlugin = (*Plugin)(nil)

func New(_ context.Context, configuration runtime.Object, fh fwk.Handle) (fwk.Plugin, error) {
	args := defaultArgs()
	if err := frameworkruntime.DecodeInto(configuration, &args); err != nil {
		return nil, err
	}
	args.normalize()

	return &Plugin{
		args:               args,
		fh:                 fh,
		reservedPodToNode:  map[string]string{},
		reservationsByNode: map[string]int64{},
	}, nil
}

func (p *Plugin) Name() string {
	return Name
}

func (p *Plugin) Less(left, right fwk.QueuedPodInfo) bool {
	leftPod := left.GetPodInfo().GetPod()
	rightPod := right.GetPodInfo().GetPod()

	leftPriority := corev1helpers.PodPriority(leftPod)
	rightPriority := corev1helpers.PodPriority(rightPod)
	if leftPriority != rightPriority {
		return leftPriority > rightPriority
	}

	if left.GetAttempts() != right.GetAttempts() {
		return left.GetAttempts() > right.GetAttempts()
	}

	return left.GetTimestamp().Before(right.GetTimestamp())
}

func (p *Plugin) Filter(_ context.Context, _ fwk.CycleState, pod *v1.Pod, nodeInfo fwk.NodeInfo) *fwk.Status {
	node := nodeInfo.Node()
	if node == nil {
		return fwk.NewStatus(fwk.UnschedulableAndUnresolvable, "node information is missing")
	}
	if node.Labels[nodeDisabledLabel] == "true" {
		return fwk.NewStatus(fwk.UnschedulableAndUnresolvable, "node is disabled for schedulab")
	}

	request := podRequest(pod)
	allocatable := nodeInfo.GetAllocatable()
	requested := nodeInfo.GetNonZeroRequested()

	if minFree := annotationInt64(pod, annotationMinFreeMilliCPU, 0); minFree > 0 {
		if allocatable.GetMilliCPU()-requested.GetMilliCPU()-request.milliCPU < minFree {
			return fwk.NewStatus(fwk.Unschedulable, "node does not keep requested free cpu")
		}
	}
	if minFree := annotationInt64(pod, annotationMinFreeMemory, 0); minFree > 0 {
		if allocatable.GetMemory()-requested.GetMemory()-request.memory < minFree {
			return fwk.NewStatus(fwk.Unschedulable, "node does not keep requested free memory")
		}
	}

	maxCPU := annotationInt64(pod, annotationMaxCPUUtilization, p.args.MaxCPUUtilization)
	if maxCPU > 0 && utilization(requested.GetMilliCPU()+request.milliCPU, allocatable.GetMilliCPU()) > maxCPU {
		return fwk.NewStatus(fwk.Unschedulable, "node cpu utilization would exceed schedulab limit")
	}

	maxMemory := annotationInt64(pod, annotationMaxMemoryUtilization, p.args.MaxMemoryUtilization)
	if maxMemory > 0 && utilization(requested.GetMemory()+request.memory, allocatable.GetMemory()) > maxMemory {
		return fwk.NewStatus(fwk.Unschedulable, "node memory utilization would exceed schedulab limit")
	}

	return nil
}

func (p *Plugin) Score(_ context.Context, _ fwk.CycleState, pod *v1.Pod, nodeInfo fwk.NodeInfo) (int64, *fwk.Status) {
	allocatable := nodeInfo.GetAllocatable()
	requested := nodeInfo.GetNonZeroRequested()
	request := podRequest(pod)

	cpuFree := remainingRatio(requested.GetMilliCPU()+request.milliCPU, allocatable.GetMilliCPU())
	memFree := remainingRatio(requested.GetMemory()+request.memory, allocatable.GetMemory())
	balance := int64(math.Round(100 - math.Abs(float64(cpuFree-memFree))))
	if balance < fwk.MinScore {
		balance = fwk.MinScore
	}

	podSpread := int64(100 - len(nodeInfo.GetPods())*4)
	if podSpread < fwk.MinScore {
		podSpread = fwk.MinScore
	}

	score := weightedAverage([]weightedScore{
		{score: cpuFree, weight: p.args.CPUWeight},
		{score: memFree, weight: p.args.MemoryWeight},
		{score: min64(balance, podSpread), weight: p.args.SpreadWeight},
	})
	return clampScore(score), nil
}

func (p *Plugin) ScoreExtensions() fwk.ScoreExtensions {
	return nil
}

func (p *Plugin) Reserve(_ context.Context, _ fwk.CycleState, pod *v1.Pod, nodeName string) *fwk.Status {
	key := podKey(pod)

	p.mu.Lock()
	defer p.mu.Unlock()

	if oldNode, ok := p.reservedPodToNode[key]; ok {
		p.decreaseReservation(oldNode)
	}
	p.reservedPodToNode[key] = nodeName
	p.reservationsByNode[nodeName]++

	return nil
}

func (p *Plugin) Unreserve(_ context.Context, _ fwk.CycleState, pod *v1.Pod, nodeName string) {
	key := podKey(pod)

	p.mu.Lock()
	defer p.mu.Unlock()

	reservedNode, ok := p.reservedPodToNode[key]
	if !ok {
		return
	}
	if reservedNode == nodeName {
		p.releaseReservation(key, nodeName)
	}
}

func (p *Plugin) Permit(_ context.Context, _ fwk.CycleState, pod *v1.Pod, nodeName string) (*fwk.Status, time.Duration) {
	if p.args.MaxReservationsPerNode <= 0 || p.args.PermitWaitSeconds <= 0 {
		return nil, 0
	}

	p.mu.Lock()
	reservations := p.reservationsByNode[nodeName]
	p.mu.Unlock()

	if reservations <= p.args.MaxReservationsPerNode {
		return nil, 0
	}

	timeout := time.Duration(p.args.PermitTimeoutSeconds) * time.Second
	wait := time.Duration(p.args.PermitWaitSeconds) * time.Second
	go p.allowAfter(pod.UID, wait)

	return fwk.NewStatus(fwk.Wait, "node has too many reserved pods"), timeout
}

func (p *Plugin) Bind(ctx context.Context, _ fwk.CycleState, pod *v1.Pod, nodeName string) *fwk.Status {
	if !p.args.BindPods {
		return fwk.NewStatus(fwk.Skip, "schedulab bind is disabled")
	}

	binding := &v1.Binding{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: pod.Namespace,
			Name:      pod.Name,
			UID:       pod.UID,
		},
		Target: v1.ObjectReference{
			Kind: "Node",
			Name: nodeName,
		},
	}

	if err := p.fh.ClientSet().CoreV1().Pods(pod.Namespace).Bind(ctx, binding, metav1.CreateOptions{}); err != nil {
		return fwk.AsStatus(err)
	}

	p.mu.Lock()
	p.releaseReservation(podKey(pod), nodeName)
	p.mu.Unlock()

	return nil
}

func (p *Plugin) EventsToRegister(_ context.Context) ([]fwk.ClusterEventWithHint, error) {
	return []fwk.ClusterEventWithHint{
		{Event: fwk.ClusterEvent{Resource: fwk.Node, ActionType: fwk.Add | fwk.UpdateNodeAllocatable | fwk.UpdateNodeTaint | fwk.UpdateNodeLabel}},
		{Event: fwk.ClusterEvent{Resource: fwk.Pod, ActionType: fwk.Delete | fwk.UpdatePodScaleDown}},
	}, nil
}

func (p *Plugin) SignPod(_ context.Context, pod *v1.Pod) ([]fwk.SignFragment, *fwk.Status) {
	request := podRequest(pod)
	return []fwk.SignFragment{
		{
			Key: "schedulab.dev/requests",
			Value: map[string]int64{
				"milliCPU": request.milliCPU,
				"memory":   request.memory,
			},
		},
		{
			Key: "schedulab.dev/annotations",
			Value: map[string]string{
				annotationMaxCPUUtilization:    pod.Annotations[annotationMaxCPUUtilization],
				annotationMaxMemoryUtilization: pod.Annotations[annotationMaxMemoryUtilization],
				annotationMinFreeMilliCPU:      pod.Annotations[annotationMinFreeMilliCPU],
				annotationMinFreeMemory:        pod.Annotations[annotationMinFreeMemory],
			},
		},
	}, nil
}

func (p *Plugin) allowAfter(uid types.UID, delay time.Duration) {
	if uid == "" {
		return
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	<-timer.C

	if waitingPod := p.fh.GetWaitingPod(uid); waitingPod != nil {
		waitingPod.Allow(Name)
	}
}

func (p *Plugin) decreaseReservation(nodeName string) {
	if p.reservationsByNode[nodeName] <= 1 {
		delete(p.reservationsByNode, nodeName)
		return
	}
	p.reservationsByNode[nodeName]--
}

func (p *Plugin) releaseReservation(podKey, nodeName string) {
	p.decreaseReservation(nodeName)
	delete(p.reservedPodToNode, podKey)
}

func defaultArgs() Args {
	return Args{
		Mode:     "balanced",
		BindPods: true,
	}
}

func (a *Args) normalize() {
	switch a.Mode {
	case "latency":
		if a.MaxCPUUtilization == 0 {
			a.MaxCPUUtilization = 100
		}
		if a.MaxMemoryUtilization == 0 {
			a.MaxMemoryUtilization = 100
		}
		if a.CPUWeight == 0 && a.MemoryWeight == 0 && a.SpreadWeight == 0 {
			a.CPUWeight = 50
			a.MemoryWeight = 50
		}
	case "precision":
		if a.MaxCPUUtilization == 0 {
			a.MaxCPUUtilization = 80
		}
		if a.MaxMemoryUtilization == 0 {
			a.MaxMemoryUtilization = 80
		}
		if a.CPUWeight == 0 && a.MemoryWeight == 0 && a.SpreadWeight == 0 {
			a.CPUWeight = 40
			a.MemoryWeight = 40
			a.SpreadWeight = 20
		}
	default:
		a.Mode = "balanced"
	}

	if a.Mode == "balanced" {
		if a.MaxCPUUtilization == 0 {
			a.MaxCPUUtilization = 90
		}
		if a.MaxMemoryUtilization == 0 {
			a.MaxMemoryUtilization = 90
		}
	}
	if a.CPUWeight == 0 && a.MemoryWeight == 0 && a.SpreadWeight == 0 {
		a.CPUWeight = 45
		a.MemoryWeight = 35
		a.SpreadWeight = 20
	}
	if a.PermitTimeoutSeconds <= a.PermitWaitSeconds {
		a.PermitTimeoutSeconds = a.PermitWaitSeconds + 5
	}
}

type podResources struct {
	milliCPU int64
	memory   int64
}

func podRequest(pod *v1.Pod) podResources {
	requests := componentresource.PodRequests(pod, componentresource.PodResourcesOptions{})
	return podResources{
		milliCPU: requests.Cpu().MilliValue(),
		memory:   requests.Memory().Value(),
	}
}

func podKey(pod *v1.Pod) string {
	if pod.UID != "" {
		return string(pod.UID)
	}
	return fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
}

func annotationInt64(pod *v1.Pod, key string, fallback int64) int64 {
	if pod.Annotations == nil {
		return fallback
	}
	raw, ok := pod.Annotations[key]
	if !ok || raw == "" {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fallback
	}
	return value
}

func utilization(used, allocatable int64) int64 {
	if allocatable <= 0 {
		return 0
	}
	return int64(math.Ceil(float64(used) * 100 / float64(allocatable)))
}

func remainingRatio(used, allocatable int64) int64 {
	if allocatable <= 0 {
		return fwk.MinScore
	}
	remaining := 100 - int64(math.Ceil(float64(used)*100/float64(allocatable)))
	return clampScore(remaining)
}

type weightedScore struct {
	score  int64
	weight int64
}

func weightedAverage(scores []weightedScore) int64 {
	var totalScore int64
	var totalWeight int64
	for _, item := range scores {
		if item.weight <= 0 {
			continue
		}
		totalScore += item.score * item.weight
		totalWeight += item.weight
	}
	if totalWeight == 0 {
		return fwk.MinScore
	}
	return int64(math.Round(float64(totalScore) / float64(totalWeight)))
}

func clampScore(score int64) int64 {
	if score < fwk.MinScore {
		return fwk.MinScore
	}
	if score > fwk.MaxScore {
		return fwk.MaxScore
	}
	return score
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
