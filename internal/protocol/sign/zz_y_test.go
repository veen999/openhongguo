package sign

import (
	"encoding/binary"
	"encoding/hex"
	"testing"
)

func TestYBranchSweep(t *testing.T) {
	qs, _ := hex.DecodeString("f460f50346643e47e818935544c6f2c2a2d66e6334ce3f6d673b754e82605b8b")
	zeroBody := make([]byte, 16)
	// Y15: branch0 layout, kh=1794399660, f13=b19242a7...
	kh15 := uint32(1794399660)
	want15, _ := hex.DecodeString("b19242a7eb1290f032b90092c39253c248a8de2f")
	ts15 := make([]byte, 4)
	binary.LittleEndian.PutUint32(ts15, kh15)
	for ivV0 := uint32(0); ivV0 < 6; ivV0++ {
		out := branch0F13(qs, zeroBody, ts15, kh15, ivV0)
		if string(out) == string(want15) {
			t.Logf("BRANCH0 HIT ivV0=%d", ivV0)
		}
	}
	// Y16: branch2 layout, kh=1794399823
	kh16 := uint32(1794399823)
	want16, _ := hex.DecodeString("247c7d3a6e4281c3e7a070529ae6faec99a67e7a")
	ts16 := make([]byte, 4)
	binary.LittleEndian.PutUint32(ts16, kh16)
	// branch2 via hashF13 path needs iv from getIV chain; call md5SumV3 route through branch2F13 with fabricated iv:
	// branch2F13(iv,...) derives ivV0 from iv; sweep iv over a range is huge. Instead directly test:
	// ivV0 in branch2 = ((v>>15)&255)+((v>>8)&255), v=(iv&13)*86. iv in [0,2^32) -> iv&13 in {0,1,4,5,8,9,12,13}
	for _, ivLow := range []uint32{0, 1, 4, 5, 8, 9, 12, 13} {
		out := branch2F13(ivLow, qs, zeroBody, ts16, kh16)
		if string(out) == string(want16) {
			t.Logf("BRANCH2 HIT iv&13=%d", ivLow)
		}
	}
	t.Log("sweep done")
}
