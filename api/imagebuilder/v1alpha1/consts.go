package imagebuilder

const (
	APIGroup = "flightctl.io"

	ImageBuildAPIVersion = "v1alpha1"
	ImageBuildListKind   = "ImageBuildList"

	ImageExportAPIVersion = "v1alpha1"
	ImageExportListKind   = "ImageExportList"

	// LogStreamCompleteMarker is sent by the server when a log stream is complete.
	// The CLI uses this to distinguish between orderly completion and abrupt disconnection.
	LogStreamCompleteMarker = "<<STREAM_COMPLETE>>"
)
