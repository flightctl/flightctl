package console

const (
	StdinID  byte = 0
	StdoutID byte = 1
	StderrID byte = 2
	ErrID    byte = 3
	ResizeID byte = 4
	CloseID  byte = 255

	// These protocols must be the same as defined in "k8s.io/apimachinery/pkg/util/remotecommand"

	StreamProtocolV5Name = "v5.channel.k8s.io"
)
