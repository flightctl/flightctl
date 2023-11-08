package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/agent"
	"github.com/flightctl/flightctl/internal/agent/controller"
	"github.com/flightctl/flightctl/internal/config"
	"k8s.io/klog/v2"
)

func main() {
	serverUrl := flag.String("server", "https://localhost:3333", "device server URL")
	metricsAddr := flag.String("metrics", "localhost:9093", "address for the metrics endpoint")
	certDir := flag.String("certs", config.CertificateDir(), "absolute path to the certificate dir")
	numDevices := flag.Int("count", 1, "number of devices to simulate")
	fetchSpecInterval := flag.Duration("fetch-spec-interval", agent.DefaultFetchSpecInterval, "Duration between two reads of the remote device spec")
	statusUpdateInterval := flag.Duration("status-update-interval", agent.DefaultStatusUpdateInterval, "Duration between two status updates")
	flag.Parse()

	klog.Infoln("starting device simulator")
	defer klog.Infoln("device simulator stopped")

	klog.Infoln("command line flags:")
	flag.CommandLine.VisitAll(func(flg *flag.Flag) {
		klog.Infof("  %s=%s", flg.Name, flg.Value)
	})

	klog.Infoln("creating agents")
	agents := make([]*agent.DeviceAgent, *numDevices)
	for i := 0; i < *numDevices; i++ {
		agentName := fmt.Sprintf("device-%04d", i)
		agentDir := filepath.Join(*certDir, agentName)
		for _, filename := range []string{"ca.crt", "client-enrollment.crt", "client-enrollment.key"} {
			if err := copyFile(filepath.Join(*certDir, filename), filepath.Join(agentDir, filename)); err != nil {
				log.Fatalf("copying %s: %v", filename, err)
			}
		}
		agents[i] = agent.NewDeviceAgent(*serverUrl, *serverUrl, agentDir).
			SetName(agentName).
			AddController(controller.NewSystemInfoController()).
			SetFetchSpecInterval(*fetchSpecInterval, 0).
			SetStatusUpdateInterval(*statusUpdateInterval, 0).
			SetRpcMetricsCallbackFunction(rpcMetricsCallback)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sigShutdown := make(chan os.Signal, 1)
	signal.Notify(sigShutdown, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigShutdown
		signal.Stop(sigShutdown)
		log.Printf("Shutdown signal received (%v).", sig)
		cancel()
	}()

	klog.Infoln("setting up metrics endpoint")
	setupMetricsEndpoint(*metricsAddr)

	klog.Infoln("running agents")
	wg := new(sync.WaitGroup)
	wg.Add(*numDevices)
	for i := 0; i < *numDevices; i++ {
		go func(i int) {
			defer wg.Done()

			// stagger the start of each agent
			time.Sleep(time.Duration(rand.Float64() * float64(*statusUpdateInterval)))

			err := agents[i].Run(ctx)
			if err != nil {
				klog.Errorf("%s: %v", agents[i].GetName(), err)
			}
		}(i)
	}
	wg.Wait()
}

func copyFile(from, to string) error {
	if _, err := os.Stat(from); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0700); err != nil {
		return err
	}
	r, err := os.Open(from)
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.Create(to)
	if err != nil {
		return err
	}
	defer w.Close()
	_, err = io.Copy(w, r)
	return err
}
