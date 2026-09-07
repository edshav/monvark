package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/edshav/monvark/util"
)

type MinerStatus struct {
	ValidShares   uint64 `json:"validShares"`
	InvalidShares uint64 `json:"invalidShares"`
	TotalShares   uint64 `json:"totalShares"`
	Started       uint32 `json:"started"`
	Uptime        uint32 `json:"uptime"`

	Devices []*DeviceStatus `json:"devices"`
}

type DeviceStatus struct {
	Index      int    `json:"index"`
	DeviceName string `json:"deviceName"`

	HashRate          float64 `json:"hashRate"`
	HashRateFormatted string  `json:"hashRateFormatted"`

	Started uint32 `json:"started"`
}

var (
	m *Miner
)

func RunMonitor(tm *Miner) {
	m = tm

	// The status API gets a mux of its own: --profile registers "/" on the
	// default one, and a second registration of the same pattern panics.
	mux := http.NewServeMux()
	mux.HandleFunc("/", getMinerStatus)
	if err := http.ListenAndServe(cfg.APIListen, mux); err != nil {
		mainLog.Warnf("Unable to create monitor: %v", err)
	}
}

func getMinerStatus(w http.ResponseWriter, _ *http.Request) {
	ms := &MinerStatus{
		Started: m.started,
		Uptime:  uint32(time.Now().Unix()) - m.started,
	}

	if !cfg.Benchmark {
		valid, invalid, total := m.Status()
		ms.ValidShares = valid
		ms.InvalidShares = invalid
		ms.TotalShares = total
	}

	for _, d := range m.devices {
		hashRate := d.Status()
		ms.Devices = append(ms.Devices, &DeviceStatus{
			Index:             d.index,
			DeviceName:        d.deviceName,
			HashRate:          hashRate,
			HashRateFormatted: util.FormatHashRate(hashRate),
			Started:           d.started,
		})
	}

	w.Header().Add("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ms)
}
