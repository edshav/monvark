// Copyright (c) 2016-2023 The Decred developers.

package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/edshav/monvark/util"
	"github.com/edshav/monvark/work"
	"github.com/monetarium/monetarium-node/chaincfg/chainhash"
	"github.com/monetarium/monetarium-node/crypto/blake256"
	chainjson "github.com/monetarium/monetarium-node/rpc/jsonrpc/types"
	"github.com/monetarium/monetarium-node/rpcclient"
	"github.com/monetarium/monetarium-node/txscript/stdaddr"
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

	// payoutAddr is the address block rewards are paid to, and payoutScript
	// its payment script.  The assertion below compares the coinbase against
	// the script; the address is what the user sees.
	payoutAddr   string
	payoutScript string
	payeeChecked bool
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

func NewMiner(ctx context.Context, devices []*Device, workDone chan []byte, payoutAddr string) (*Miner, error) {
	var m *Miner
	var err error
	if cfg.Benchmark {
		m = &Miner{devices: devices}
	} else {
		m, err = newSoloMiner(ctx, devices)
	}
	if err != nil {
		return nil, err
	}

	if payoutAddr != "" {
		addr, err := stdaddr.DecodeAddress(payoutAddr, chainParams)
		if err != nil {
			return nil, err
		}
		_, script := addr.PaymentScript()
		m.payoutAddr = payoutAddr
		m.payoutScript = hex.EncodeToString(script)
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
			hash := chainhash.Hash(blake256.Sum256(data[:180]))
			minrLog.Infof("Submitted work successfully: block hash %v", hash)

			if err := m.checkPayee(ctx, &hash); err != nil {
				minrLog.Criticalf("%v", err)
				return
			}
		}
	}
}

// checkPayee reads back a block this miner submitted and asserts its coinbase
// pays the user.  It runs once, on the first accepted block: this is the only
// point in the system that covers where the money goes rather than whether the
// hash is right.
//
// It is evidence and a bug detector, not a defence against a substituted node
// binary -- a hostile node lies on every RPC it serves.  The archive's SHA256
// is what covers that.
func (m *Miner) checkPayee(ctx context.Context, hash *chainhash.Hash) error {
	if m.payeeChecked || m.payoutScript == "" {
		return nil
	}

	blk, err := m.rpc.GetBlockVerbose(ctx, hash, true)
	if err != nil {
		// A block we cannot read back is not evidence of theft, so warn rather
		// than stopping, and try again on the next one.
		minrLog.Warnf("Unable to read back block %v to verify the payee: %v",
			hash, err)
		return nil
	}

	if !coinbasePays(blk, m.payoutScript) {
		return fmt.Errorf("block %v does not pay %s -- refusing to mine "+
			"further.  The node monvark started was given this address on "+
			"its own command line, so this should be impossible; check that "+
			"the mond binary beside monvark is the one from the release "+
			"archive", hash, m.payoutAddr)
	}

	m.payeeChecked = true
	minrLog.Infof("Coinbase verified: block %d pays %s", blk.Height,
		m.payoutAddr)
	return nil
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

// coinbasePays reports whether the coinbase of blk pays wantScript.
//
// The comparison is against the raw payment script rather than the address
// string that also appears in the result: comparing scripts is the exact check
// the design wanted, and it survives address-encoding variations that string
// matching would not.  A coinbase carries the treasury and other outputs
// alongside the miner's, so ours being one of several is the normal case.
func coinbasePays(blk *chainjson.GetBlockVerboseResult, wantScript string) bool {
	// An empty want-script must never match.  ScriptPubKey.Hex is omitempty,
	// so it can decode as "", and EqualFold("", "") is true -- this function
	// decides whether the user was paid and must fail closed, not open.
	if wantScript == "" {
		return false
	}
	if len(blk.RawTx) == 0 {
		return false
	}
	for _, out := range blk.RawTx[0].Vout {
		if strings.EqualFold(out.ScriptPubKey.Hex, wantScript) {
			return true
		}
	}
	return false
}

// Status returns the miner's accepted, rejected and total share counts.
func (m *Miner) Status() (uint64, uint64, uint64) {
	valid := atomic.LoadUint64(&m.validShares)
	rejected := atomic.LoadUint64(&m.invalidShares)
	return valid, rejected, valid + rejected
}
