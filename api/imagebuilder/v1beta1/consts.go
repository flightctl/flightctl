package imagebuilder

const (
	APIGroup = "flightctl.io"

	ImageBuildAPIVersion = "v1beta1"
	ImageBuildListKind   = "ImageBuildList"

	ImageExportAPIVersion = "v1beta1"
	ImageExportListKind   = "ImageExportList"

	// LogStreamCompleteMarker is sent by the server when a log stream is complete.
	// The CLI uses this to distinguish between orderly completion and abrupt disconnection.
	LogStreamCompleteMarker = "<<STREAM_COMPLETE>>"
)
