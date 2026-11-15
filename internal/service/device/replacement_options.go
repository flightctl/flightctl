package device

// ReplaceOption configures the semantics of a Device replacement.
type ReplaceOption func(*replaceOptions)

type replaceOptions struct {
	preserveOmittedFirmware bool
}

func newReplaceOptions(opts ...ReplaceOption) replaceOptions {
	options := replaceOptions{preserveOmittedFirmware: true}
	for _, opt := range opts {
		opt(&options)
	}
	return options
}

// WithOmittedFirmwareRemoval makes a supplied replacement spec that omits
// firmware withdraw firmware management. It is used by API versions whose PUT
// semantics treat omission as removal.
func WithOmittedFirmwareRemoval() ReplaceOption {
	return func(options *replaceOptions) {
		options.preserveOmittedFirmware = false
	}
}
