package sign

import (
	"crypto/sha256"
	"encoding/binary"
	"sync"
	"sync/atomic"
	"time"
)

// Real-device shaping for x-medusa, ported from upstream openhongguo
// v0.4.1 (commit 50baf58, decoded from the 7.4.5.32 app on 2026-11-23).
// Since the 2026-11-23 playback rule flip, the service judges the
// medusa plaintext's device environment: the sparse emulator-shaped
// block (missing storage/memory/clocks, placeholder strings in typed
// fields) is answered with an empty body alongside the field-21 status
// rejection fixed in 8134661. The layout below is the one the SDK
// reports on a phone; values the request itself states (model, brand,
// OS, app version) come from the request, storage/memory/install time
// are derived from the device ID so one device reports the same values
// in every process, and what only a phone can supply stays "!notset!".

const (
	medusaNoPerm    = "!noperm!"
	medusaNoPermInt = -888888
	medusaNotSet    = "!notset!"
	medusaUnsetCtr  = 694367
	// medusaInstallAnchor is 2026-11-23 13:33 UTC, the day of upstream's
	// capture; install times spread over the 20 days before it.
	medusaInstallAnchor = 1795440816000
	// Environment clock offsets (fields 18-20 minus the request
	// timestamp) as in upstream's DefaultMedusaEnv.
	medusaClock18 = 766353820
	medusaClock19 = 16
	medusaClock20 = -1072050540
)

var medusaDeviceFlags = []byte{0x04, 0x05, 0, 0, 0, 0, 0, 0}

// Per-process IDs: a real process has its own PID/PPID pair, stable for
// the process's life instead of the emulator's fixed pair.
var (
	medusaProcPID  = int64(4096 + time.Now().UnixNano()%20000)
	medusaProcPPID = int64(900 + (time.Now().UnixNano()/20000)%1200)
)

type medusaDeviceShape struct {
	InstallMillis                            int64
	StorageGB, StorageFreeGB                 float32
	MemoryGB, MemoryFreeGB                   float32
}

// medusaShapeOf derives one device's plausible storage, memory and
// install time from its device ID (upstream's derivation, verbatim).
func medusaShapeOf(deviceID string) medusaDeviceShape {
	h := sha256.Sum256([]byte("medusa-device\x00" + deviceID))
	pick := func(i int, n uint32) uint32 { return binary.LittleEndian.Uint32(h[i*4:]) % n }
	storage := []float32{106.83, 108.57, 229.36, 233.91, 487.62}[pick(0, 5)] + float32(pick(1, 400))/1000
	memory := []float32{5.37, 7.42, 11.18, 15.07}[pick(2, 4)] + float32(pick(3, 90))/1000
	const day = 24 * 60 * 60 * 1000
	return medusaDeviceShape{
		InstallMillis: medusaInstallAnchor - day - int64(pick(4, 20*day)),
		StorageGB:     storage,
		StorageFreeGB: storage * (0.25 + float32(pick(5, 500))/1000),
		MemoryGB:      memory,
		MemoryFreeGB:  0.35 + float32(pick(6, 900))/1000,
	}
}

var (
	medusaCounterOnce sync.Once
	medusaCounter     atomic.Int64
)

// nextMedusaCount is message field 15's first counter: on a device it
// grows by one with each signature of the process.
func nextMedusaCount() int64 {
	medusaCounterOnce.Do(func() { medusaCounter.Store(20 + time.Now().UnixNano()%100) })
	return medusaCounter.Add(1)
}

func protoEmpty(tag uint32) []byte {
	return append(protoKey(tag, 2), 0)
}

// medusaDeviceBlock is environment field 12 in a real device's layout
// (upstream medusaV5DeviceBlock). field5 keeps the caller's existing
// nonce value; the two builders each have one that shipped.
func medusaDeviceBlock(deviceID, field5, model, brand, osVersion string, started, nowMillis int64) []byte {
	d := medusaShapeOf(deviceID)
	str := func(tag uint32, s string) []byte {
		if s == "" {
			s = medusaNotSet
		}
		return protoString(tag, s)
	}
	return concat(
		protoSInt(1, 1),
		protoString(3, appID),
		protoString(4, deviceID),
		protoString(5, field5),
		protoString(6, medusaNoPerm),
		protoSInt(7, medusaNoPermInt),
		protoSInt(8, medusaNoPermInt),
		protoSInt(9, 2),
		protoSInt(10, medusaNoPermInt),
		protoString(11, medusaNotSet),
		protoString(12, "Asia/Shanghai,8"),
		protoString(13, "zh_CN"),
		protoSInt(14, 8),
		protoFloat(16, d.StorageGB),
		protoFloat(17, d.StorageFreeGB),
		protoFloat(18, d.MemoryGB),
		protoFloat(19, d.MemoryFreeGB),
		protoFloat(20, d.StorageGB*2),
		protoFloat(21, d.StorageFreeGB*2),
		str(22, osVersion),
		protoSInt(23, 60),
		protoSInt(24, 10),
		protoSInt(25, d.InstallMillis),
		protoSInt(26, started+540),
		protoSInt(27, d.InstallMillis),
		protoSInt(28, nowMillis),
		protoSInt(29, 1),
		str(30, model),
		str(31, brand),
		protoString(32, medusaNotSet),
		protoString(33, medusaNotSet),
		protoString(34, medusaNotSet),
		str(35, brand),
		protoString(36, medusaNotSet),
		protoSInt(38, 3),
		protoSInt(40, started),
	)
}

// medusaEnvDevice is environment field 23 in a real device's layout:
// uptime first, the process fields, and the three sampled clocks.
// envVersion is the caller's own SDK generation string.
func medusaEnvDevice(ts uint32, envVersion, channel string, block []byte, version string, started int64) []byte {
	uptime := int64(ts) - started/1000
	if uptime < 1 {
		uptime = 1
	}
	if version == "" {
		version = medusaNotSet
	}
	return concat(
		protoSInt(1, uptime),
		protoString(6, envVersion),
		protoSInt(7, medusaProcPID),
		protoString(8, channel),
		protoBytes(12, block),
		protoEmpty(13),
		protoString(14, version),
		protoSInt(15, medusaProcPPID),
		protoSInt(18, int64(ts)+medusaClock18),
		protoSInt(19, int64(ts)+medusaClock19),
		protoSInt(20, int64(ts)+medusaClock20),
	)
}
