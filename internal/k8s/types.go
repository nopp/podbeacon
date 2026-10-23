package k8s

import "time"

// Report is one unhealthy pod, plus the events and logs gathered for it.
type Report struct {
	UID        string
	Namespace  string
	Name       string
	Node       string
	Phase      string
	Age        time.Duration
	QoS        string
	Owners     []string
	Labels     []string
	Containers []Container
	Conditions []string
	Reasons    []Reason
	Severity   int
	// LogContainers are the containers whose logs are worth reading, worst first.
	LogContainers []string
	Events        []string
	EventNote     string
	Logs          []LogSnippet
}

// Container is a compact view of one container status.
type Container struct {
	Name      string
	Init      bool
	Image     string
	Ready     bool
	Restarts  int32
	StateLine string
	LastLine  string
}

// Reason is one problem signal. Code is stable across polls; Text includes
// the current message and is not part of the cooldown key.
type Reason struct {
	Code      string
	Text      string
	Severity  int
	Container string
}

// LogSnippet is a log tail or a note explaining why it could not be read.
type LogSnippet struct {
	Container string
	Previous  bool
	Text      string
	Note      string
}

// Key returns namespace/name.
func (r Report) Key() string {
	return r.Namespace + "/" + r.Name
}
