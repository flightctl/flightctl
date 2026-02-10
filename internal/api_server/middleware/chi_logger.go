package middleware

import (
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"

	apiversioning "github.com/flightctl/flightctl/api/versioning"
	"github.com/flightctl/flightctl/internal/api_server/versioning"
	chimw "github.com/go-chi/chi/v5/middleware"
)

type apiVersionTaggingLogger struct {
	base       chimw.LoggerInterface
	apiVersion string
}

// Print intercepts log messages and injects the API version tag.
func (l apiVersionTaggingLogger) Print(v ...any) {
	if len(v) != 1 {
		l.base.Print(v...)
		return
	}

	// Chi logs format:
	// "GET http://example.com/path HTTP/1.1" from 192.0.2.1:1234 - 200 0B in 462ns.
	// This finds " from ", and injects the version tag before it.
	// It's hacky, but allows reusing the existing logger
	if s, ok := v[0].(string); ok {
		tag := " " + l.apiVersion
		if i := strings.Index(s, " from "); i >= 0 {
			l.base.Print(s[:i] + tag + s[i:])
			return
		}
		l.base.Print(s + tag)
		return
	}

	l.base.Print(v...)
}

// apiVersionTag returns a formatted API version tag for logging.
// Returns "(<version>)" if valid, "(missing)" if empty, or "(invalid)" otherwise.
func apiVersionTag(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "(missing)"
	}
	v := versioning.Version(raw)
	if v.IsValid() {
		return "(" + string(v) + ")"
	}
	return "(invalid)"
}

type apiVersionLogFormatter struct {
	Logger  chimw.LoggerInterface
	NoColor bool
}

func (f *apiVersionLogFormatter) NewLogEntry(r *http.Request) chimw.LogEntry {
	apiVersion := apiVersionTag(r.Header.Get(apiversioning.HeaderAPIVersion))
	logger := apiVersionTaggingLogger{base: f.Logger, apiVersion: apiVersion}

	df := &chimw.DefaultLogFormatter{Logger: logger, NoColor: f.NoColor}
	return df.NewLogEntry(r)
}

func ChiLoggerWithAPIVersionTag() func(http.Handler) http.Handler {
	stdLogger := log.New(os.Stdout, "", log.LstdFlags)
	noColor := runtime.GOOS == "windows"

	return chimw.RequestLogger(&apiVersionLogFormatter{
		Logger:  stdLogger,
		NoColor: noColor,
	})
}

func ChiLogFormatterWithAPIVersionTag(logger chimw.LoggerInterface) chimw.LogFormatter {
	return &apiVersionLogFormatter{
		Logger: logger,
	}
}
