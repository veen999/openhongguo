package sign

import (
	"encoding/binary"
	"encoding/hex"
	"testing"
)

func branch0K(qs, body, ts []byte, khronos, K uint32, row int) []byte {
	ivV1 := K
	countV1 := (ivV1 + khronos + 1) & 255
	countV2 := ivV1 + khronos
	tt := make([]uint32, len(branch0TT))
	for i, v := range branch0TT {
		tt[i] = ror32(v, countV1)
	}
	n0 := (countV2 + 2) & 7
	seed := []byte{0xfa, 0x45, 0x61, 0xd7}
	pad := make([]byte, len(seed))
	for i, v := range seed {
		combined := uint16(v) | (uint16(v) << 8)
		pad[i] = byte((combined >> n0) & 255)
	}
	data := append([]byte{}, qs...)
	data = append(data, body...)
	data = append(data, ts...)
	data = append(data, pad...)
	tail, _ := hex.DecodeString("00000000000001a0")
	data = append(data, tail...)
	di := expandWords(data)
	init := make([]uint32, len(branch0Init))
	for i, v := range branch0Init {
		init[i] = ror32(v, countV2&31)
	}
	d := mixRounds(append([]uint32{}, init...), branch0Rounds[row], di, tt, ivV1)
	ret := make([]byte, 0, 32)
	for i := 0; i < 8; i++ {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], d[i]+init[i])
		ret = append(ret, b[:]...)
	}
	for i := 0; i < 16; i++ {
		ret[i] = ret[i] ^ ret[16+i]
	}
	out := append([]byte{}, ret[:16]...)
	return append(out, le32(sumMD5(ret[:16]))...)
}

func branch2K(qs, body, ts []byte, khronos, n1 uint32, ivV0 int) []byte {
	countV1 := (n1 + khronos + 1) & 255
	countV2 := n1 + khronos
	shift := (countV2 + 5) & 7
	seed := []byte{0x84, 0x96, 0x77, 0x9d, 0xd4, 0x15, 0x0b, 0xf8}
	pad := make([]byte, len(seed))
	for i, value := range seed {
		combined := uint16(value) | (uint16(value) << 8)
		pad[i] = byte((combined >> shift) & 255)
	}
	input := append([]byte{}, qs...)
	input = append(input, body...)
	input = append(input, ts...)
	input = append(input, pad...)
	tail, _ := hex.DecodeString("a0010000")
	input = append(input, tail...)
	all := branch2Orders()
	begin := ivV0 << 6
	if begin+64 > len(all) {
		return nil
	}
	return md5SumV3(input, countV2, all[begin:begin+64], countV1)
}

func TestYParamSweep(t *testing.T) {
	qs, _ := hex.DecodeString("f460f50346643e47e818935544c6f2c2a2d66e6334ce3f6d673b754e82605b8b")
	zeroBody := make([]byte, 16)
	// Branch0 / Y15
	kh15 := uint32(1794399660)
	want15, _ := hex.DecodeString("b19242a7eb1290f032b90092c39253c248a8de2f")
	ts15 := make([]byte, 4)
	binary.LittleEndian.PutUint32(ts15, kh15)
	hits := 0
	for K := uint32(0); K < 256; K++ {
		if (K+kh15+2)&7 != 3 {
			continue
		}
		for row := 0; row < 6; row++ {
			out := branch0K(qs, zeroBody, ts15, kh15, K, row)
			if string(out) == string(want15) {
				t.Logf("BRANCH0 HIT K=%d row=%d", K, row)
				hits++
			}
		}
	}
	// Branch2 / Y16
	kh16 := uint32(1794399823)
	want16, _ := hex.DecodeString("247c7d3a6e4281c3e7a070529ae6faec99a67e7a")
	ts16 := make([]byte, 4)
	binary.LittleEndian.PutUint32(ts16, kh16)
	for n1 := uint32(0); n1 < 256; n1++ {
		if (n1+kh16+5)&7 != 6 {
			continue
		}
		for ivV0 := 0; ivV0 < 64; ivV0++ {
			out := branch2K(qs, zeroBody, ts16, kh16, n1, ivV0)
			if out != nil && string(out) == string(want16) {
				t.Logf("BRANCH2 HIT n1mod256=%d ivV0=%d", n1, ivV0)
				hits++
			}
		}
	}
	t.Logf("done hits=%d ordersLen=%d", hits, len(branch2Orders()))
}
