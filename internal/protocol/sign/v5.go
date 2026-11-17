package sign

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// The mobile v5 playback signing protocol (official client metasec
// v04.10.03). On 2026-11-16 the platform retired the previous playback
// chains outright: the video_tab GET and the batch/singular POSTs —
// including the ones carrying real Argus/Ladon — answer HTTP 200 with
// an empty body for every identity. The replacement, published by the
// hongguo-open-player 0.1.32 fix (2026-11-16) and independently ported
// by hongguoTV, moves the model read to
// POST /novel/player/multi_video_model/player/v1 with a gzipped body
// and a three-header signature: x-khronos, x-helios and x-medusa only.
// The old x-argus / x-ladon / x-gorgon / x-ss-req-ticket headers are
// forgery markers under v5 and must not be sent. The medusa digest is
// pinned to the iv&15==12 branch and the medusa/helios/AesV3 constant
// tables are the v5 set in v5const.go. Everything else in this package
// (catalog, account, old playback signing) is unchanged.

// V5Identity is the device identity the v5 medusa plaintext embeds. It
// must match the request's query values field for field: the service
// cross-checks the decrypted plaintext against the query.
type V5Identity struct {
	DeviceID    string
	VersionName string
	Channel     string
	OSVersion   string
	DeviceType  string
}

// SignPlaybackV5 signs one v5 playback request. pairs are the query
// pairs in wire order WITHOUT _rticket; the ticket is advanced from
// nowMillis until the signature digest lands on the iv&15==12 branch
// (the only branch v5 accepts). body is the exact gzipped request
// body that will be sent. The returned headers carry only the v5 set
// plus Content-Type and the body stub; the caller adds User-Agent,
// Accept and Content-Encoding.
func SignPlaybackV5(pairs [][2]string, nowMillis int64, id V5Identity, body []byte, entropy *Entropy) (string, map[string]string) {
	ent := Entropy{}
	if entropy != nil {
		ent = *entropy
	} else {
		ent = drawEntropy(nowMillis)
	}
	khronos := uint32(nowMillis / 1000)
	var query string
	for offset := int64(0); offset < 1024; offset++ {
		ticket := nowMillis + offset
		withTicket := append([][2]string{}, pairs...)
		withTicket = append(withTicket, [2]string{"_rticket", strconv.FormatInt(ticket, 10)})
		query = encodeQuery(withTicket)
		if v5BranchOK(query, body, khronos) {
			break
		}
	}
	headers := map[string]string{
		"x-khronos":    strconv.FormatUint(uint64(khronos), 10),
		"x-helios":     assembleHeliosV5(khronos, ent.HeliosRandom),
		"x-medusa":     assembleMedusaV5(query, body, khronos, id, ent),
		"Content-Type": "application/json; charset=utf-8",
	}
	if body != nil {
		sum := md5Raw(body)
		headers["x-ss-stub"] = strings.ToUpper(hex.EncodeToString(sum[:]))
	}
	return query, headers
}

// v5BranchOK reports whether the (query, body, khronos) digest lands
// on branch 12, the only branch the v5 medusa digest can express.
func v5BranchOK(query string, body []byte, khronos uint32) bool {
	sum := sm3Sum([]byte(query))
	bodyMD5 := make([]byte, 16)
	if body != nil {
		s := md5Raw(body)
		bodyMD5 = s[:]
	}
	iv := getIV(getIV(getIV(0x20230928, sum[:]), bodyMD5), le32(khronos))
	return iv&15 == 12
}

// hashF13V5 is the v5 medusa digest: the branch-0 compression with the
// branch pinned to iv&15==12 — a fixed round constant (0x9d4a97c2),
// the round table rotated by count+1, the state rotated by count, the
// fixed round permutation, and a 0x20 marker ORed into the checksum.
func hashF13V5(querySM3, bodyMD5 []byte, khronos uint32) []byte {
	const ivV1 = uint32(0x9d4a97c2)
	count := ivV1 + khronos
	tt := make([]uint32, len(branch0TT))
	for i, v := range branch0TT {
		tt[i] = ror32(v, (count+1)&255)
	}
	shift := (count + 2) & 7
	seed := []byte{0xfa, 0x45, 0x61, 0xd7}
	pad := make([]byte, len(seed))
	for i, v := range seed {
		combined := uint16(v) | (uint16(v) << 8)
		pad[i] = byte((combined >> shift) & 255)
	}
	data := append([]byte{}, querySM3...)
	data = append(data, bodyMD5...)
	data = append(data, le32(khronos)...)
	data = append(data, pad...)
	tail, _ := hex.DecodeString("00000000000001a0")
	data = append(data, tail...)
	di := expandWords(data)
	init := make([]uint32, len(branch0Init))
	for i, v := range branch0Init {
		init[i] = ror32(v, count)
	}
	d := mixRounds(append([]uint32{}, init...), [11]uint32{96, 1, 3, 4, 0, 6, 5, 2, 0, 7, 7}, di, tt, ivV1)
	ret := make([]byte, 0, 32)
	for i := 0; i < 8; i++ {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], d[i]+init[i])
		ret = append(ret, b[:]...)
	}
	folded := make([]byte, 16)
	for i := 0; i < 16; i++ {
		folded[i] = ret[i] ^ ret[16+i]
	}
	return append(folded, le32(sumMD5(folded)|0x20)...)
}

// assembleHeliosV5 is assembleHelios with the v5 license id embedded
// in the padded text.
func assembleHeliosV5(khronos, random uint32) string {
	seed := append(le32(random), []byte(appID)...)
	digest := md5Raw(seed)
	ascii := []byte(hex.EncodeToString(digest[:]))
	words := make([]uint64, 4)
	for i := 0; i < 4; i++ {
		words[i] = binary.LittleEndian.Uint64(ascii[i*8 : i*8+8])
	}
	table := diffuse(words)
	text := []byte(fmt.Sprintf("%d-%s-%s", khronos, v5License, appID))
	fill := byte(16 - (len(text) % 16))
	size := ((len(text) + 1 + 15) / 16) * 16
	padded := make([]byte, size)
	for i := range padded {
		padded[i] = fill
	}
	copy(padded, text)
	var output []byte
	for off := 0; off < len(padded); off += 16 {
		chunk := padded[off : off+16]
		a := binary.LittleEndian.Uint64(chunk[:8])
		b := binary.LittleEndian.Uint64(chunk[8:])
		for _, t := range table[:34] {
			b = t ^ (a + ror64(b, 8))
			a = b ^ ror64(a, 61)
		}
		var ab [16]byte
		binary.LittleEndian.PutUint64(ab[:8], a)
		binary.LittleEndian.PutUint64(ab[8:], b)
		output = append(output, ab[:]...)
	}
	result := append(le32(random), output...)
	return base64.StdEncoding.EncodeToString(result)
}

func deviceProtoV5(id V5Identity) []byte {
	var out []byte
	out = append(out, protoSInt(1, 1)...)
	out = append(out, protoSInt(2, 2)...)
	out = append(out, protoString(3, appID)...)
	out = append(out, protoString(4, id.DeviceID)...)
	out = append(out, protoString(5, MedusaNonce)...)
	out = append(out, protoString(6, "!noperm!")...)
	out = append(out, protoString(12, "Asia/Shanghai,8")...)
	out = append(out, protoString(13, "zh_CN")...)
	out = append(out, protoString(22, id.OSVersion)...)
	out = append(out, protoSInt(25, MedusaDeviceStartedMS)...)
	out = append(out, protoString(30, id.DeviceType)...)
	out = append(out, protoString(31, "HONOR")...)
	out = append(out, protoString(32, id.DeviceType)...)
	out = append(out, protoString(34, "HONOR")...)
	return out
}

func envV5(id V5Identity) []byte {
	var out []byte
	out = append(out, protoString(6, "v04.10.03.04-bugfix")...)
	out = append(out, protoString(8, id.Channel)...)
	out = append(out, protoBytes(12, deviceProtoV5(id))...)
	out = append(out, protoString(14, id.VersionName)...)
	return out
}

// assembleMedusaV5 builds the v04.10.03 medusa envelope: the v5
// plaintext layout, magic and license, the pinned-branch digest, and
// the v5 AesV3 carrier keyed by md5 halves of the v5 sign key.
func assembleMedusaV5(query string, body []byte, khronos uint32, id V5Identity, ent Entropy) string {
	querySM3 := sm3Sum([]byte(query))
	bodyMD5 := make([]byte, 16)
	if body != nil {
		s := md5Raw(body)
		bodyMD5 = s[:]
	}
	f13 := hashF13V5(querySM3[:], bodyMD5, khronos)
	flags := make([]byte, 8)

	nested := append(protoSInt(1, 1), protoSInt(2, 1)...)
	nested = append(nested, protoSInt(3, 1)...)
	nested = append(nested, protoSInt(5, 1)...)

	message := protoBytes(1, v5Magic)
	message = append(message, protoSInt(2, 5)...)
	message = append(message, protoSInt(3, int64(ent.MessageRandom))...)
	message = append(message, protoString(4, appID)...)
	message = append(message, protoString(5, id.DeviceID)...)
	message = append(message, protoString(6, v5License)...)
	message = append(message, protoString(7, id.VersionName)...)
	message = append(message, protoString(8, "v04.10.03-ml-android")...)
	message = append(message, protoSInt(9, 0x040a0300)...)
	message = append(message, protoBytes(10, flags)...)
	message = append(message, protoSInt(12, int64(khronos))...)
	message = append(message, protoBytes(13, f13)...)
	message = append(message, protoBytes(14, querySM3[:6])...)
	message = append(message, protoBytes(15, nested)...)
	message = append(message, protoSInt(17, int64(khronos))...)
	message = append(message, protoString(20, "none")...)
	message = append(message, protoSInt(21, 377)...)
	message = append(message, protoBytes(23, envV5(id))...)
	message = append(message, protoString(24, `{"cmr":16777216,"cmr2":16777216,"un_h":11,"vpn":0,"kd":0,"dyn":"","do":1,"tk":true}`)...)

	hash, seed := keyHash(v5SignKey, ent.Random)
	transformed := append(append([]byte{}, flags...), xmxor(message, hash[:])...)
	for i, j := 0, len(transformed)-1; i < j; i, j = i+1, j-1 {
		transformed[i], transformed[j] = transformed[j], transformed[i]
	}
	for i := range transformed {
		transformed[i] ^= seed[(^i)&3]
	}
	check := uint32(0x18000001) | (uint32(querySM3[0]&63) << 14) | (uint32(f13[0]&63) << 8)
	packed := []byte{0xca}
	packed = append(packed, le32(ent.PackedRandom)...)
	packed = append(packed, le32(check)...)
	packed = append(packed, transformed...)
	packed = append(packed, byte(ent.Random>>16), byte(ent.Random>>24))

	keyA := md5Raw(v5SignKey[:16])
	keyB := md5Raw(v5SignKey[16:])
	encrypted := newAESV3V5(keyA[:], khronos).encryptV5(packed, keyB[:])

	prefix := append(le32(5), v5Magic...)
	tsLE := le32(khronos)
	for i := range prefix {
		prefix[i] ^= tsLE[i&3]
	}
	out := append([]byte{}, prefix...)
	out = append(out, byte(ent.Random&255), byte((ent.Random>>8)&255), 0, 1)
	out = append(out, encrypted...)
	return base64.StdEncoding.EncodeToString(out)
}

var aesCon2V5 = [4][4]int{{2, 0, 3, 1}, {1, 3, 2, 0}, {3, 2, 1, 0}, {2, 3, 1, 0}}

var aesOrderV5 = [4][16]int{
	{0, 9, 14, 15, 4, 13, 2, 7, 8, 1, 6, 3, 12, 5, 10, 11},
	{0, 9, 14, 11, 4, 13, 2, 3, 8, 1, 6, 15, 12, 5, 10, 7},
	{0, 9, 14, 3, 4, 13, 2, 15, 8, 1, 6, 11, 12, 5, 10, 7},
	{0, 9, 14, 15, 4, 13, 2, 3, 8, 1, 6, 11, 12, 5, 10, 7},
}

var aesInitWordV5 = [4]uint32{0xbbb5de72, 0xd4638d5b, 0x92a5f73b, 0x89f8a1c9}

var aesWireV5 = [8]int{6, 5, 3, 4, 2, 7, 1, 0}

var aesCompactV5 = [8]int{1, 7, 3, 6, 2, 0, 4, 5}

func newAESV3V5(key []byte, khronos uint32) aesV3 {
	word := int(khronos & 3)
	all := v5Sbox()
	start := word * 256
	return aesV3{
		box:   append([]byte{}, all[start:start+256]...),
		con2:  aesCon2V5[word],
		order: aesOrderV5[word],
		keys:  expandAESV5(key, word),
	}
}

func expandAESV5(key []byte, word int) [12][4]byte {
	all := v5Sbox()
	box := all[word*256 : word*256+256]
	init := aesInitWordV5[word]
	initial := make([]byte, 0, 16)
	var le [4]byte
	binary.LittleEndian.PutUint32(le[:], init)
	for i := 0; i < 4; i++ {
		initial = append(initial, le[:]...)
	}
	expanded := xorBytes(initial, key)
	expanded = append(expanded, make([]byte, 32)...)
	rounds := uint32(8)
	for i := 4; i < 12; i++ {
		at := 4 * (i - 1)
		a, b, c, d := expanded[at], expanded[at+1], expanded[at+2], expanded[at+3]
		if i&3 == 0 {
			t := ((init >> (rounds & 24)) ^ uint32(box[b])) & 255
			b = box[c]
			c = box[d]
			d = box[a]
			a = byte(t)
		}
		rounds += 2
		expanded[at+4] = a ^ expanded[at-12]
		expanded[at+5] = b ^ expanded[at-11]
		expanded[at+6] = c ^ expanded[at-10]
		expanded[at+7] = d ^ expanded[at-9]
	}
	var keys [12][4]byte
	for i := range keys {
		copy(keys[i][:], expanded[i*4:i*4+4])
	}
	return keys
}

// encryptV5 is the AesV3 carrier with the v5 bit-routing tables. The
// block cipher itself is aesV3.block; only the plaintext gather and
// keystream scatter positions differ from aesV3.encrypt.
func (a aesV3) encryptV5(data, iv []byte) []byte {
	src := func(at int) byte {
		if at < len(data) {
			return data[at]
		}
		return 0
	}
	var plaintext [32]byte
	for i := 0; i < 31; i++ {
		var v byte
		for j := 0; j < 8; j++ {
			v |= byte((uint32(src(i*8+j))>>uint(aesWireV5[j]))&1) << uint(aesCompactV5[j])
		}
		plaintext[i] = v
	}
	plaintext[31] = 1
	first := a.block(xorBytes(plaintext[0:16], iv))
	keyStream := append(append([]byte{}, first...), a.block(xorBytes(plaintext[16:32], first))...)
	out := append([]byte{}, data...)
	for i := 0; i < 31; i++ {
		for j := 0; j < 8; j++ {
			at := i*8 + j
			if at >= len(out) {
				continue
			}
			k := keyStream[i]
			out[at] = (out[at] &^ (1 << uint(aesWireV5[j]))) |
				(byte((uint32(k)>>uint(aesCompactV5[j]))&1) << uint(aesWireV5[j]))
		}
	}
	return append([]byte{keyStream[31]}, out...)
}
