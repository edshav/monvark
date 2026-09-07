// Copyright (c) 2016-2023 The Decred developers.

package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/decred/gominer/blake3"
	"github.com/decred/gominer/util"
	"github.com/decred/gominer/work"
	"github.com/monetarium/monetarium-node/blockchain/standalone"
	"github.com/monetarium/monetarium-node/chaincfg"
	"github.com/monetarium/monetarium-node/chaincfg/chainhash"
)

var chainParams = chaincfg.MainNetParams()

// randDeviceOffset1 and randDeviceOffset2 are random offsets to use for all
// devices so each process ends up with a random starting point for all devices.
var randDeviceOffset1, randDeviceOffset2 uint8

func init() {
	var buf [2]byte
	if _, err := io.ReadFull(rand.Reader, buf[:]); err != nil {
		panic(err)
	}
	randDeviceOffset1 = buf[0]
	randDeviceOffset2 = buf[1]
}

// Device type strings as reported by CL_DEVICE_TYPE.
const (
	DeviceTypeCPU = "CPU"
	DeviceTypeGPU = "GPU"
)

// initNonces initialize the nonces for the device such that each device in the
// same system is doing different work while also helping prevent collisions
// across multiple processes and systems working on the same template.
func (d *Device) initNonces() error {
	// Read cryptographically random data for use below in setting the initial
	// nonces.
	var buf [8]byte
	if _, err := io.ReadFull(rand.Reader, buf[:]); err != nil {
		return fmt.Errorf("unable to read random value: %w", err)
	}
	extraNonceRandOffset := binary.LittleEndian.Uint32(buf[0:])
	extraNonce2RandOffset := binary.LittleEndian.Uint32(buf[4:])

	// Set the extra nonce to a random value.  This value is unique per device
	// when solo mining.  The extra nonce is assigned by the pool instead when
	// pool mining.
	//
	// When combined with the device ID in the second extra nonce below, this
	// helps prevent collisions across multiple processes and systems working on
	// the same template.
	d.extraNonce = extraNonceRandOffset

	// Set the initial second extra nonce as follows:
	// - The first byte is the device ID offset by the first per-process random
	//   device offset
	// - The remaining 3 bytes are a per-device random extra nonce offset
	//
	// This ensures each device in the same system is doing different work (up
	// to 256 devices).
	//
	// This implies that the total search space is 7 bytes when combining the 3
	// bytes provided by this value along with the normal 4-byte nonce.  In
	// other words, it supports devices up to ~72 Ph/s.
	deviceOffset := (uint32(d.index) + uint32(randDeviceOffset1)) % 256
	d.extraNonce2 = deviceOffset<<24 | extraNonce2RandOffset&0x00ffffff

	minrLog.Debugf("DEV #%d: initial extraNonce %x, initial extraNonce2: %x",
		d.index, d.extraNonce, d.extraNonce2)
	return nil
}

func (d *Device) updateCurrentWork(ctx context.Context) {
	var w *work.Work
	if d.hasWork {
		// If we already have work, we just need to check if there's new one
		// without blocking if there's not.
		select {
		case w = <-d.newWork:
		default:
			return
		}
	} else {
		// If we don't have work, we block until we do.
		select {
		case w = <-d.newWork:
		case <-ctx.Done():
			return
		}
	}

	d.hasWork = true

	d.work = *w
	minrLog.Tracef("pre-nonce: %x", d.work.Data[:])

	// Ensure the work data is updated with the extra nonce associated with the
	// device for solo mining.
	//
	// The extra nonce is provided by the pool when pool mining, so there is no
	// need to update it in that case.
	const en1Offset = 128 + 4*work.Nonce1Word
	if d.work.IsGetWork {
		binary.LittleEndian.PutUint32(d.work.Data[en1Offset:], d.extraNonce)
	}

	// Ensure the work data is updated with the second extra nonce associated
	// with the device.
	binary.LittleEndian.PutUint32(d.work.Data[128+4*work.Nonce2Word:],
		d.extraNonce2)

	// Set additional byte with the device id offset by a second per-process
	// random device offset to support up to 65536 devices with getwork (solo)
	// mining.  Pool mining does not support the additional byte, so it is not
	// needed in that case.  Note that this also means pool mining only supports
	// 256 devices per client (aka process instance).
	if d.work.IsGetWork {
		deviceID := uint8((uint32(d.index) + uint32(randDeviceOffset2)) % 256)
		d.work.Data[128+4*work.Nonce3Word] = deviceID
	}

	// Hash the two first blocks.
	d.midstate = blake3.Block(blake3.IV, d.work.Data[0:64], blake3.FlagChunkStart)
	d.midstate = blake3.Block(d.midstate, d.work.Data[64:128], 0)
	minrLog.Tracef("midstate input data for work update %x", d.work.Data[0:128])

	// Convert the next block to uint32 array.
	for i := 0; i < 16; i++ {
		d.lastBlock[i] = binary.LittleEndian.Uint32(d.work.Data[128+i*4:])
	}
	minrLog.Tracef("work data for work update: %x", d.work.Data)
}

func (d *Device) Run(ctx context.Context) {
	err := d.runDevice(ctx)
	if err != nil {
		minrLog.Errorf("Error on device: %v", err)
	}
}

func (d *Device) foundCandidate(ts, nonce0, nonce1, nonce2 uint32) {
	d.Lock()
	defer d.Unlock()
	// Construct the final block header.
	data := make([]byte, 192)
	copy(data, d.work.Data[:])

	binary.LittleEndian.PutUint32(data[128+4*work.TimestampWord:], ts)
	binary.LittleEndian.PutUint32(data[128+4*work.Nonce0Word:], nonce0)
	binary.LittleEndian.PutUint32(data[128+4*work.Nonce1Word:], nonce1)
	binary.LittleEndian.PutUint32(data[128+4*work.Nonce2Word:], nonce2)
	hash := chainhash.Hash(blake3.FinalBlock(d.midstate, data[128:180]))

	// The kernel applies no target of its own: it discards everything whose
	// final 32-bit hash word is non-zero (blake3.cl, "if (v7 ^ v15) return;")
	// and leaves the target comparison to the host.  So every candidate that
	// reaches here must hash to a value ending in a zero word.  One that does
	// not means the host and the GPU disagree about the hash -- a miscompiled
	// kernel, a wrong midstate, or a byte-order slip -- rather than an unlucky
	// nonce.
	if binary.LittleEndian.Uint32(hash[28:]) != 0 {
		minrLog.Errorf("DEV #%d: GPU and host disagree on the hash of a "+
			"candidate: %v does not end in a zero word", d.index, hash)
		d.hashMismatches++
		d.invalidShares++
		return
	}

	// Hashes that reach this logic and fail the minimal proof of
	// work check are considered to be hardware errors.
	hashNum := standalone.HashToBig(&hash)
	if hashNum.Cmp(chainParams.PowLimit) > 0 {
		minrLog.Errorf("DEV #%d: Hardware error found, hash %v above "+
			"minimum target %064x", d.index, hash, chainParams.PowLimit)
		d.invalidShares++
		return
	}

	d.allDiffOneShares++

	if !cfg.Benchmark {
		// Assess versus the pool or daemon target.
		if hashNum.Cmp(d.work.Target) > 0 {
			minrLog.Debugf("DEV #%d: Hash %v bigger than target %064x (boo)",
				d.index, hash, d.work.Target)
		} else {
			minrLog.Infof("DEV #%d: Found hash with work below target! %v (yay)",
				d.index, hash)
			d.validShares++
			d.workDone <- data
		}
	}
}

func (d *Device) SetWork(ctx context.Context, w *work.Work) {
	select {
	case d.newWork <- w:
	case <-ctx.Done():
	}
}

func (d *Device) PrintStats() {
	minrLog.Infof("DEV #%d (%s) %v", d.index, d.deviceName,
		util.FormatHashRate(d.Status()))
}

// Status returns the average hash rate of the device since it was started.
func (d *Device) Status() float64 {
	secondsElapsed := uint32(time.Now().Unix()) - d.started
	if secondsElapsed == 0 {
		return 0
	}

	// allDiffOneShares is incremented by foundCandidate under this lock, and
	// Status is called both from the stats goroutine and from the status API,
	// so the read has to take it too.  The previous code read it unlocked from
	// both.
	d.Lock()
	shares := d.allDiffOneShares
	d.Unlock()

	const diffOneShareHashesAvg = float64(0x00000000FFFFFFFF)
	return (diffOneShareHashesAvg * float64(shares)) / float64(secondsElapsed)
}
