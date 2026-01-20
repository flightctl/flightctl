package middleware

import (
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"

	"github.com/flightctl/flightctl/internal/api_server/versioning"
	chimw "github.com/go-chi/chi/v5/middleware"
)

type apiVersionTaggingLogger struct {
	base       chimw.LoggerInterface
	apiVersion string
}

// Print intercepts log messages and injects the API version tag.
func (l apiVersionTaggingLogger) Print(v ...interface{}) {
	if l.apiVersion == "" || len(v) != 1 {
		l.base.Print(v...)
		return
	}

	// Chi logs format: "HTTP/1.1 200 OK from 127.0.0.1". This finds " from "
	// and injects the version before it: "HTTP/1.1 200 OK (v1alpha1) from 127.0.0.1"

	// It's hacky, but allows reusing the existing logger
	if s, ok := v[0].(string); ok {
		tag := " (" + l.apiVersion + ")"
		if i := strings.Index(s, " from "); i >= 0 {
			l.base.Print(s[:i] + tag + s[i:])
			return
		}
		l.base.Print(s + tag)
		return
	}

	l.base.Print(v...)
}

type apiVersionLogFormatter struct {
	Logger  chimw.LoggerInterface
	NoColor bool
}

func (f *apiVersionLogFormatter) NewLogEntry(r *http.Request) chimw.LogEntry {
	apiVersion := strings.TrimSpace(r.Header.Get(versioning.HeaderAPIVersion))
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
