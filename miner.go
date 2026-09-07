// Copyright (c) 2016-2023 The Decred developers.

package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/edshav/monvark/util"
	"github.com/edshav/monvark/work"
	"github.com/monetarium/monetarium-node/chaincfg/chainhash"
	"github.com/monetarium/monetarium-node/crypto/blake256"
	"github.com/monetarium/monetarium-node/rpcclient"
)

type Miner struct {
	// The following variables must only be used atomically.
	validShares   uint64
	invalidShares uint64

	started  uint32
	devices  []*Device
	workDone chan []byte
	wg       sync.WaitGroup

	rpc *rpcclient.Client
}

// onSoloWork prepares the provided getwork-based work data, which might have
// either come from getwork directly or from asynchronous work notifications,
// and updates all of the provided devices with that prepared work.
func onSoloWork(ctx context.Context, data, target []byte, reason string, devices []*Device) {
	minrLog.Debugf("Work received: (data: %x, target: %x, reason: %s)", data,
		target, reason)

	// The bigTarget difficulty is provided in little endian, but big integers
	// expect big endian, so reverse it accordingly.
	bigTarget := new(big.Int).SetBytes(util.Reverse(target))

	var workData [192]byte
	copy(workData[:], data)

	timestamp := binary.LittleEndian.Uint32(workData[128+4*work.TimestampWord:])
	w := &work.Work{
		Data:         workData,
		Target:       bigTarget,
		JobTime:      timestamp,
		TimeReceived: uint32(time.Now().Unix()),
	}

	for _, d := range devices {
		d.SetWork(ctx, w)
	}
}

func newSoloMiner(ctx context.Context, devices []*Device) (*Miner, error) {
	var rpc *rpcclient.Client
	ntfnHandlers := rpcclient.NotificationHandlers{
		OnBlockConnected: func(blockHeader []byte, transactions [][]byte) {
			minrLog.Infof("Block connected: %x (%d transactions)", blockHeader, len(transactions))
		},
		OnBlockDisconnected: func(blockHeader []byte) {
			minrLog.Infof("Block disconnected: %x", blockHeader)
		},
		OnWork: func(data, target []byte, reason string) {
			onSoloWork(ctx, data, target, reason, devices)
		},
	}
	// Connect to local dcrd RPC server using websockets.
	certs, err := os.ReadFile(cfg.RPCCert)
	if err != nil {
		return nil, fmt.Errorf("failed to read rpc certificate %v: %w",
			cfg.RPCCert, err)
	}

	connCfg := &rpcclient.ConnConfig{
		Host:         cfg.RPCServer,
		Endpoint:     "ws",
		User:         cfg.RPCUser,
		Pass:         cfg.RPCPassword,
		Certificates: certs,
		Proxy:        cfg.Proxy,
		ProxyUser:    cfg.ProxyUser,
		ProxyPass:    cfg.ProxyPass,
	}
	rpc, err = rpcclient.New(connCfg, &ntfnHandlers)
	if err != nil {
		return nil, err
	}
	err = rpc.NotifyWork(ctx)
	if err != nil {
		rpc.Shutdown()
		return nil, err
	}
	err = rpc.NotifyBlocks(ctx)
	if err != nil {
		rpc.Shutdown()
		return nil, err
	}
	m := &Miner{
		devices: devices,
		rpc:     rpc,
	}

	return m, nil
}

func NewMiner(ctx context.Context) (*Miner, error) {
	workDone := make(chan []byte, 10)

	devices, err := newMinerDevs(workDone)
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("no devices started")
	}

	var m *Miner
	if cfg.Benchmark {
		m = &Miner{devices: devices}
	} else {
		m, err = newSoloMiner(ctx, devices)
	}
	if err != nil {
		return nil, err
	}

	m.workDone = workDone
	m.started = uint32(time.Now().Unix())

	// Return early on benchmark mode to avoid requiring a dcrd instance to
	// be running.
	if cfg.Benchmark {
		return m, nil
	}

	// Perform an initial call to getwork so work is available immediately.
	workResult, err := m.rpc.GetWork(ctx)
	if err != nil {
		m.rpc.Shutdown()
		return nil, fmt.Errorf("unable to retrieve initial work: %w", err)
	}

	data, err := hex.DecodeString(workResult.Data)
	if err != nil {
		m.rpc.Shutdown()
		return nil, fmt.Errorf("unable to decode work data: %w", err)
	}
	target, err := hex.DecodeString(workResult.Target)
	if err != nil {
		m.rpc.Shutdown()
		return nil, fmt.Errorf("unable to decode work target: %w", err)
	}
	onSoloWork(ctx, data, target, "initialwork", devices)

	return m, nil
}

func (m *Miner) workSubmitThread(ctx context.Context) {
	defer m.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case data := <-m.workDone:
			accepted, err := m.rpc.GetWorkSubmit(ctx, hex.EncodeToString(data))
			if err != nil {
				atomic.AddUint64(&m.invalidShares, 1)
				minrLog.Errorf("failed to submit work: %v", err)
				continue
			} else if !accepted {
				atomic.AddUint64(&m.invalidShares, 1)
				minrLog.Error("work not accepted")
				continue
			}
			atomic.AddUint64(&m.validShares, 1)
			minrLog.Infof("Submitted work successfully: block hash %v",
				chainhash.Hash(blake256.Sum256(data[:180])))
		}
	}
}

func (m *Miner) printStatsThread(ctx context.Context) {
	defer m.wg.Done()

	t := time.NewTicker(time.Second * 5)
	defer t.Stop()

	for {
		if !cfg.Benchmark {
			valid, rejected, total := m.Status()
			minrLog.Infof("Global stats: Accepted: %v, Rejected: %v, Total: %v",
				valid, rejected, total)
		}

		for _, d := range m.devices {
			d.PrintStats()
		}

		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (m *Miner) Run(ctx context.Context) {
	m.wg.Add(len(m.devices))

	for _, d := range m.devices {
		device := d
		go func() {
			device.Run(ctx)
			device.Release()
			m.wg.Done()
		}()
	}

	m.wg.Add(1)
	go m.workSubmitThread(ctx)

	if cfg.Benchmark {
		minrLog.Warn("Running in BENCHMARK mode! No real mining taking place!")
		work := &work.Work{}
		for _, d := range m.devices {
			d.SetWork(ctx, work)
		}
	}

	m.wg.Add(1)
	go m.printStatsThread(ctx)

	m.wg.Wait()
}

// Status returns the miner's accepted, rejected and total share counts.
func (m *Miner) Status() (uint64, uint64, uint64) {
	valid := atomic.LoadUint64(&m.validShares)
	rejected := atomic.LoadUint64(&m.invalidShares)
	return valid, rejected, valid + rejected
}
