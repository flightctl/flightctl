package profiling

import (
	"context"

	"github.com/flightctl/flightctl/internal/config"
	instpprof "github.com/flightctl/flightctl/internal/instrumentation/pprof"
	"github.com/grafana/pyroscope-go"
	"github.com/sirupsen/logrus"
)

// Start starts the profiling backends enabled in cfg.
//
//   - profiling.pprof.enabled → loopback net/http/pprof
//   - profiling.pyroscope.enabled → push profiles to a Pyroscope server
//
// applicationName is used for Pyroscope when profiling.pyroscope.applicationName is unset
// (e.g. "flightctl-worker"). defaultPprofPort is used when profiling.pprof.port is unset.
func Start(ctx context.Context, log logrus.FieldLogger, cfg *config.Config, applicationName string, defaultPprofPort int) {
	if cfg == nil {
		return
	}
	if cfg.PprofProfilingEnabled() {
		port := cfg.PprofProfilingPort(defaultPprofPort)
		if log != nil {
			log.Infof("profiling: starting pprof on loopback port %d", port)
		}
		instpprof.StartInBackground(ctx, log, true, port)
	}
	if pc := cfg.PyroscopeProfilingConfig(); pc != nil {
		startPyroscope(ctx, log, pc, applicationName)
	}
}

func startPyroscope(ctx context.Context, log logrus.FieldLogger, pc *config.PyroscopeProfilingConfig, defaultAppName string) {
	if pc.ServerAddress == "" {
		if log != nil {
			log.Error("profiling: pyroscope.enabled is true but profiling.pyroscope.serverAddress is empty; not starting pyroscope")
		}
		return
	}
	appName := pc.ApplicationName
	if appName == "" {
		appName = defaultAppName
	}
	if appName == "" {
		if log != nil {
			log.Error("profiling: pyroscope application name is empty; not starting pyroscope")
		}
		return
	}

	if log != nil {
		log.Infof("profiling: starting pyroscope push to %s as %q", pc.ServerAddress, appName)
	}

	profiler, err := pyroscope.Start(pyroscope.Config{
		ApplicationName:   appName,
		ServerAddress:     pc.ServerAddress,
		BasicAuthUser:     pc.BasicAuthUser,
		BasicAuthPassword: pc.BasicAuthPassword.Value(),
		TenantID:          pc.TenantID,
		Logger:            pyroscopeLogger{log: log},
		ProfileTypes: []pyroscope.ProfileType{
			pyroscope.ProfileCPU,
			pyroscope.ProfileAllocObjects,
			pyroscope.ProfileAllocSpace,
			pyroscope.ProfileInuseObjects,
			pyroscope.ProfileInuseSpace,
		},
	})
	if err != nil {
		if log != nil {
			log.WithError(err).Error("profiling: failed to start pyroscope")
		}
		return
	}

	go func() {
		<-ctx.Done()
		if err := profiler.Stop(); err != nil && log != nil {
			log.WithError(err).Warn("profiling: pyroscope stop error")
		}
	}()
}

type pyroscopeLogger struct {
	log logrus.FieldLogger
}

func (l pyroscopeLogger) Infof(format string, args ...interface{}) {
	if l.log != nil {
		l.log.Infof(format, args...)
	}
}

func (l pyroscopeLogger) Debugf(format string, args ...interface{}) {
	if l.log != nil {
		l.log.Debugf(format, args...)
	}
}

func (l pyroscopeLogger) Errorf(format string, args ...interface{}) {
	if l.log != nil {
		l.log.Errorf(format, args...)
	}
}
