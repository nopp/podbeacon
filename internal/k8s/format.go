package k8s

import (
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Problem is the first reason, including the API message. The model sees this.
// Slack uses Headline so the channel does not get the raw error.
func (r Report) Problem() string {
	if len(r.Reasons) == 0 {
		return ""
	}
	text := r.Reasons[0].Text
	if extra := len(r.Reasons) - 1; extra > 0 {
		text = fmt.Sprintf("%s · +%d", text, extra)
	}
	return text
}

// Headline is the reason without the kubelet message that follows ": ".
func (r Report) Headline() string {
	if len(r.Reasons) == 0 {
		return ""
	}
	head := r.Reasons[0].Text
	if i := strings.Index(head, ": "); i >= 0 {
		head = head[:i]
	}
	head = strings.TrimSpace(head)
	head = strings.Replace(head, " no init container ", " · ", 1)
	head = strings.Replace(head, " no container ", " · ", 1)
	if extra := len(r.Reasons) - 1; extra > 0 {
		head = fmt.Sprintf("%s · +%d", head, extra)
	}
	return head
}

// Image is the image of the container named by the first reason.
func (r Report) Image() string {
	name := ""
	if len(r.Reasons) > 0 {
		name = r.Reasons[0].Container
	}
	if name == "" {
		return ""
	}
	for _, c := range r.Containers {
		if c.Name == name && c.Image != "" {
			return c.Image
		}
	}
	return ""
}

// Restarts is the highest restart count among the containers that are failing.
func (r Report) Restarts() int32 {
	return r.problemRestarts()
}

// Signal is the one-line summary used when the model call fails.
func (r Report) Signal() string {
	var parts []string
	for i, reason := range r.Reasons {
		if i == 3 {
			parts = append(parts, fmt.Sprintf("and %d more", len(r.Reasons)-3))
			break
		}
		parts = append(parts, reason.Text)
	}
	var bits []string
	if len(parts) > 0 {
		bits = append(bits, strings.Join(parts, "; "))
	}
	if r.Phase != "" {
		bits = append(bits, "phase "+r.Phase)
	}
	if r.Node != "" {
		bits = append(bits, "node "+r.Node)
	}
	if n := r.problemRestarts(); n > 0 {
		if n == 1 {
			bits = append(bits, "1 restart")
		} else {
			bits = append(bits, fmt.Sprintf("%d restarts", n))
		}
	}
	return strings.Join(bits, " · ")
}

func (r Report) problemRestarts() int32 {
	names := map[string]struct{}{}
	for _, reason := range r.Reasons {
		if reason.Container != "" {
			names[reason.Container] = struct{}{}
		}
	}
	if len(names) == 0 {
		return 0
	}
	var max int32
	for _, c := range r.Containers {
		if _, ok := names[c.Name]; !ok {
			continue
		}
		if c.Restarts > max {
			max = c.Restarts
		}
	}
	return max
}

// Fingerprint identifies the problem set. Messages are excluded on purpose:
// a CrashLoop backoff string changes every poll.
func (r Report) Fingerprint() string {
	codes := make([]string, len(r.Reasons))
	for i, reason := range r.Reasons {
		codes[i] = reason.Code
	}
	sort.Strings(codes)
	return r.Key() + "|" + strings.Join(codes, ",")
}

// Evidence is the text sent to the model.
func (r Report) Evidence() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Pod: %s/%s\n", r.Namespace, r.Name)
	if len(r.Owners) > 0 {
		fmt.Fprintf(&b, "Owner: %s\n", strings.Join(r.Owners, ", "))
	}
	if r.Node != "" {
		fmt.Fprintf(&b, "Node: %s\n", r.Node)
	}
	fmt.Fprintf(&b, "Phase: %s\n", emptyDash(r.Phase))
	fmt.Fprintf(&b, "Age: %s\n", humanDur(r.Age))
	if r.QoS != "" {
		fmt.Fprintf(&b, "QoS: %s\n", r.QoS)
	}
	if len(r.Labels) > 0 {
		fmt.Fprintf(&b, "Labels: %s\n", strings.Join(r.Labels, ", "))
	}
	if len(r.Reasons) > 0 {
		b.WriteString("Signals:\n")
		for _, reason := range r.Reasons {
			fmt.Fprintf(&b, "- %s\n", reason.Text)
		}
	}
	writeContainers(&b, r.Containers)
	if len(r.Conditions) > 0 {
		b.WriteString("Conditions:\n")
		conds := r.Conditions
		if len(conds) > 8 {
			conds = conds[:8]
		}
		for _, c := range conds {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}
	switch {
	case r.EventNote != "":
		fmt.Fprintf(&b, "Events: unavailable (%s)\n", r.EventNote)
	case len(r.Events) > 0:
		b.WriteString("Events:\n")
		ev := r.Events
		if len(ev) > 12 {
			ev = ev[len(ev)-12:]
		}
		for _, e := range ev {
			fmt.Fprintf(&b, "- %s\n", e)
		}
	}
	writeLogs(&b, r.Logs)
	return b.String()
}

func writeContainers(b *strings.Builder, containers []Container) {
	if len(containers) == 0 {
		return
	}
	b.WriteString("Containers:\n")
	show := len(containers)
	if show > 20 {
		show = 20
	}
	for _, c := range containers[:show] {
		kind := "container"
		if c.Init {
			kind = "init"
		}
		fmt.Fprintf(b, "- %s %s image=%s ready=%t restarts=%d\n", kind, c.Name, emptyDash(c.Image), c.Ready, c.Restarts)
		if c.StateLine != "" {
			fmt.Fprintf(b, "  state: %s\n", c.StateLine)
		}
		if c.LastLine != "" {
			fmt.Fprintf(b, "  previous: %s\n", c.LastLine)
		}
	}
	if len(containers) > show {
		fmt.Fprintf(b, "- and %d more containers\n", len(containers)-show)
	}
}

func writeLogs(b *strings.Builder, logs []LogSnippet) {
	if len(logs) == 0 {
		return
	}
	if len(logs) > 4 {
		logs = logs[:4]
	}
	b.WriteString("Logs:\n")
	for _, lg := range logs {
		label := lg.Container
		if lg.Previous {
			label += " (previous)"
		}
		switch {
		case lg.Text != "":
			fmt.Fprintf(b, "- %s:\n%s\n", label, clip(lg.Text, 6000))
		case lg.Note != "":
			fmt.Fprintf(b, "- %s: log unavailable (%s)\n", label, lg.Note)
		}
	}
}

func formatEvent(e corev1.Event, now time.Time) string {
	count := e.Count
	if e.Series != nil && e.Series.Count > count {
		count = e.Series.Count
	}
	msg := clip(oneLine(e.Message), 300)
	when := eventTime(e)
	age := "?"
	if !when.IsZero() {
		age = humanDur(now.Sub(when))
	}
	if count > 1 {
		return fmt.Sprintf("%s %s %s (x%d) %s", age, e.Type, e.Reason, count, msg)
	}
	return fmt.Sprintf("%s %s %s %s", age, e.Type, e.Reason, msg)
}

func eventTime(e corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
}

func stateLine(s corev1.ContainerState) string {
	switch {
	case s.Waiting != nil:
		msg := clip(oneLine(s.Waiting.Message), 180)
		if msg == "" {
			return "waiting " + s.Waiting.Reason
		}
		return "waiting " + s.Waiting.Reason + ": " + msg
	case s.Terminated != nil:
		msg := clip(oneLine(s.Terminated.Message), 180)
		line := fmt.Sprintf("terminated %s exit=%d", s.Terminated.Reason, s.Terminated.ExitCode)
		if msg != "" {
			line += ": " + msg
		}
		return line
	case s.Running != nil:
		return "running"
	default:
		return ""
	}
}

func humanDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
