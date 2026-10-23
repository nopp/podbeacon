package k8s

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Hard waiting reasons are already a failure; the startup grace does not apply.
var hardWaiting = map[string]int{
	"CrashLoopBackOff":           100,
	"ImagePullBackOff":           90,
	"ErrImagePull":               90,
	"CreateContainerConfigError": 92,
	"CreateContainerError":       80,
	"InvalidImageName":           85,
	"RunContainerError":          80,
	"DeadlineExceeded":           70,
}

// Soft waiting reasons are normal while a pod starts.
var softWaiting = map[string]int{
	"ContainerCreating": 40,
	"PodInitializing":   40,
	"CreateContainer":   40,
}

var terminatedSeverity = map[string]int{
	"OOMKilled":          95,
	"Error":              65,
	"ContainerCannotRun": 80,
	"StartError":         80,
	"DeadlineExceeded":   70,
	"Evicted":            60,
}

var labelKeys = []string{
	"app",
	"app.kubernetes.io/name",
	"app.kubernetes.io/version",
	"app.kubernetes.io/component",
}

type hit struct {
	severity  int
	code      string
	text      string
	container string
}

// Inspect reports whether pod is unhealthy right now.
// Pods that are deleting or already succeeded are ignored.
// Image pull, crash, config and OOM failures alert immediately.
// Pending, unschedulable and slow container creation wait for grace.
func Inspect(pod *corev1.Pod, now time.Time, grace time.Duration) (Report, bool) {
	if pod == nil {
		return Report{}, false
	}
	r := skeleton(pod, now)
	if pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodSucceeded {
		return r, false
	}

	var hits []hit
	for i := range pod.Status.InitContainerStatuses {
		hits = append(hits, statusHits(pod.Status.InitContainerStatuses[i], true, r.Age, grace)...)
	}
	for i := range pod.Status.ContainerStatuses {
		hits = append(hits, statusHits(pod.Status.ContainerStatuses[i], false, r.Age, grace)...)
	}
	hits = append(hits, podHits(pod, r.Age, grace, len(hits) > 0)...)
	if len(hits) == 0 {
		return r, false
	}

	sortHits(hits)
	seen := map[string]struct{}{}
	for _, h := range hits {
		if _, ok := seen[h.code]; ok {
			continue
		}
		seen[h.code] = struct{}{}
		r.Reasons = append(r.Reasons, Reason{
			Code:      h.code,
			Text:      h.text,
			Severity:  h.severity,
			Container: h.container,
		})
		if h.severity > r.Severity {
			r.Severity = h.severity
		}
		if h.container != "" && !containsStr(r.LogContainers, h.container) && len(r.LogContainers) < 2 {
			r.LogContainers = append(r.LogContainers, h.container)
		}
	}
	if len(r.LogContainers) == 0 {
		r.LogContainers = notReadyContainers(pod, 2)
	}
	return r, true
}

func statusHits(c corev1.ContainerStatus, init bool, age, grace time.Duration) []hit {
	if h, ok := waitingHit(c, init, age, grace); ok {
		return []hit{h}
	}
	if h, ok := terminatedHit(c, init); ok {
		return []hit{h}
	}
	return nil
}

func waitingHit(c corev1.ContainerStatus, init bool, age, grace time.Duration) (hit, bool) {
	w := c.State.Waiting
	if w == nil {
		return hit{}, false
	}
	reason := w.Reason
	if reason == "" {
		reason = "Waiting"
	}
	sev, hard := hardWaiting[reason]
	softSev, soft := softWaiting[reason]
	switch {
	case hard:
	case soft && age >= grace:
		sev = softSev
	case !soft && age >= grace:
		sev = 50
	default:
		return hit{}, false
	}
	return newHit(sev, reason, c.Name, init, w.Message, 0, false), true
}

func terminatedHit(c corev1.ContainerStatus, init bool) (hit, bool) {
	tm := c.State.Terminated
	if tm == nil {
		return hit{}, false
	}
	if tm.ExitCode == 0 && (tm.Reason == "" || tm.Reason == "Completed") {
		return hit{}, false
	}
	reason := tm.Reason
	if reason == "" {
		reason = "Error"
	}
	sev, ok := terminatedSeverity[reason]
	if !ok {
		sev = 60
	}
	return newHit(sev, reason, c.Name, init, tm.Message, tm.ExitCode, true), true
}

func newHit(sev int, reason, container string, init bool, message string, exit int32, withExit bool) hit {
	kind := "c"
	where := "container"
	if init {
		kind = "init"
		where = "init container"
	}
	text := fmt.Sprintf("%s no %s %s", reason, where, container)
	if withExit {
		text += fmt.Sprintf(" (exit %d)", exit)
	}
	if msg := clip(oneLine(message), 180); msg != "" {
		text += ": " + msg
	}
	return hit{
		severity:  sev,
		code:      kind + ":" + container + ":" + stableReason(reason),
		text:      text,
		container: container,
	}
}

func podHits(pod *corev1.Pod, age, grace time.Duration, haveContainer bool) []hit {
	var hits []hit
	switch pod.Status.Phase {
	case corev1.PodFailed:
		hits = append(hits, failedHit(pod))
	case corev1.PodUnknown:
		hits = append(hits, hit{severity: 75, code: "pod:Unknown", text: "Fase Unknown"})
	}
	unschedMsg, unsched := unschedulableMessage(pod)
	if unsched && age >= grace {
		text := "Unschedulable"
		if msg := clip(oneLine(unschedMsg), 180); msg != "" {
			text += ": " + msg
		}
		hits = append(hits, hit{severity: 55, code: "pod:Unschedulable", text: text})
	}
	if pod.Status.Phase == corev1.PodPending && !haveContainer && !unsched && !initInProgress(pod) && age >= grace {
		hits = append(hits, hit{
			severity: 35,
			code:     "pod:Pending",
			text:     "Pending há " + humanDur(age),
		})
	}
	if pod.Status.Phase == corev1.PodRunning && !haveContainer && age >= grace && !podReady(pod) {
		hits = append(hits, hit{
			severity: 45,
			code:     "pod:NotReady",
			text:     "NotReady há " + humanDur(age),
		})
	}
	return hits
}

func failedHit(pod *corev1.Pod) hit {
	code := "pod:Failed"
	text := "Fase Failed"
	if pod.Status.Reason != "" {
		code += ":" + pod.Status.Reason
		text = fmt.Sprintf("Fase Failed (%s)", pod.Status.Reason)
	}
	if pod.Status.Message != "" {
		text += ": " + clip(oneLine(pod.Status.Message), 180)
	}
	return hit{severity: 70, code: code, text: text}
}

func unschedulableMessage(pod *corev1.Pod) (string, bool) {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
			return c.Message, true
		}
	}
	return "", false
}

func initInProgress(pod *corev1.Pod) bool {
	for _, c := range pod.Status.InitContainerStatuses {
		if c.State.Running != nil {
			return true
		}
		if c.State.Waiting != nil {
			switch c.State.Waiting.Reason {
			case "PodInitializing", "ContainerCreating", "":
				return true
			}
		}
	}
	for _, c := range pod.Status.ContainerStatuses {
		if c.State.Waiting != nil && c.State.Waiting.Reason == "PodInitializing" {
			return true
		}
	}
	return false
}

func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func notReadyContainers(pod *corev1.Pod, n int) []string {
	var out []string
	add := func(list []corev1.ContainerStatus) {
		for _, c := range list {
			if c.Ready || len(out) >= n {
				continue
			}
			out = append(out, c.Name)
		}
	}
	add(pod.Status.ContainerStatuses)
	add(pod.Status.InitContainerStatuses)
	if len(out) == 0 && n > 0 && len(pod.Status.ContainerStatuses) > 0 {
		out = append(out, pod.Status.ContainerStatuses[0].Name)
	}
	return out
}

func skeleton(pod *corev1.Pod, now time.Time) Report {
	r := Report{
		UID:       string(pod.UID),
		Namespace: pod.Namespace,
		Name:      pod.Name,
		Node:      pod.Spec.NodeName,
		Phase:     string(pod.Status.Phase),
		Age:       ageOf(pod, now),
		QoS:       string(pod.Status.QOSClass),
	}
	for _, o := range pod.OwnerReferences {
		r.Owners = append(r.Owners, o.Kind+"/"+o.Name)
	}
	for _, key := range labelKeys {
		if v, ok := pod.Labels[key]; ok && v != "" {
			r.Labels = append(r.Labels, key+"="+v)
		}
	}
	r.Containers = append(r.Containers, snaps(pod, true)...)
	r.Containers = append(r.Containers, snaps(pod, false)...)
	for _, c := range pod.Status.Conditions {
		if c.Status == corev1.ConditionTrue && c.Type != corev1.PodReady {
			continue
		}
		r.Conditions = append(r.Conditions, conditionLine(c))
	}
	return r
}

func snaps(pod *corev1.Pod, init bool) []Container {
	var stats []corev1.ContainerStatus
	if init {
		stats = pod.Status.InitContainerStatuses
	} else {
		stats = pod.Status.ContainerStatuses
	}
	out := make([]Container, 0, len(stats))
	for _, c := range stats {
		image := c.Image
		if image == "" {
			image = specImage(pod, c.Name, init)
		}
		snap := Container{
			Name:      c.Name,
			Init:      init,
			Image:     image,
			Ready:     c.Ready,
			Restarts:  c.RestartCount,
			StateLine: stateLine(c.State),
		}
		if hasState(c.LastTerminationState) {
			snap.LastLine = stateLine(c.LastTerminationState)
		}
		out = append(out, snap)
	}
	return out
}

func specImage(pod *corev1.Pod, name string, init bool) string {
	specs := pod.Spec.Containers
	if init {
		specs = pod.Spec.InitContainers
	}
	for _, c := range specs {
		if c.Name == name {
			return c.Image
		}
	}
	return ""
}

func conditionLine(c corev1.PodCondition) string {
	line := fmt.Sprintf("%s=%s", c.Type, c.Status)
	if c.Reason != "" {
		line += " " + c.Reason
	}
	if msg := clip(oneLine(c.Message), 180); msg != "" {
		line += ": " + msg
	}
	return line
}

func ageOf(pod *corev1.Pod, now time.Time) time.Duration {
	if pod.CreationTimestamp.IsZero() {
		return 0
	}
	age := now.Sub(pod.CreationTimestamp.Time)
	if age < 0 {
		return 0
	}
	return age
}

func hasState(s corev1.ContainerState) bool {
	return s.Waiting != nil || s.Running != nil || s.Terminated != nil
}

func stableReason(reason string) string {
	if reason == "ErrImagePull" {
		return "ImagePullBackOff"
	}
	return reason
}

func sortHits(hits []hit) {
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hitLess(hits[j], hits[j-1]); j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
}

func hitLess(a, b hit) bool {
	if a.severity != b.severity {
		return a.severity > b.severity
	}
	return a.code < b.code
}

func containsStr(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
