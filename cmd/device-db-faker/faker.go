package main

import (
	"context"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/labels"
)

type DeviceDBFaker struct {
	log   *logrus.Logger
	store store.Store
}

func NewFaker() *DeviceDBFaker {
	log := flightlog.InitLogs()
	fmt.Printf("Config file: %s\n", config.ConfigFile())
	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	fmt.Printf("Config: %v\n", cfg)
	if err != nil {
		log.Fatalf("reading configuration: %v", err)
	}

	db, err := store.InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing data store: %v", err)
	}

	store := store.NewStore(db, log.WithField("pkg", "store"))

	return &DeviceDBFaker{
		log:   log,
		store: store,
	}
}

func (d *DeviceDBFaker) Close() {
	d.store.Close()
}

func (d *DeviceDBFaker) UpdateStatuses(ctx context.Context, status *api.DeviceStatus, labelSelector string) error {
	orgId := store.NullOrgId

	labelMap, err := labels.ConvertSelectorToLabelsMap(labelSelector)
	if err != nil {
		return fmt.Errorf("invalid label selector: %w", err)
	}

	devList, err := d.store.Device().List(ctx, orgId, store.ListParams{Labels: labelMap})
	if err != nil {
		return fmt.Errorf("failed fetching devices: %w", err)
	}

	status.LastSeen = time.Now()
	for i := range devList.Items {
		dev := devList.Items[i]
		dev.Status = status
		_, err = d.store.Device().UpdateStatus(ctx, orgId, &dev)
		if err != nil {
			return fmt.Errorf("failed updating device %s status: %w", *dev.Metadata.Name, err)
		}
	}

	return nil
}
