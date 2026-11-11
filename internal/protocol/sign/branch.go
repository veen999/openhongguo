package sign

import (
	"strconv"
	"strings"
)

// BranchOfSigned recomputes the medusa branch a signed query landed on.
// body is the exact request body (nil for a bodyless GET).
func BranchOfSigned(query string, body []byte) uint32 {
	var ticket uint64
	for _, part := range strings.Split(query, "&") {
		if strings.HasPrefix(part, "_rticket=") {
			ticket, _ = strconv.ParseUint(strings.TrimPrefix(part, "_rticket="), 10, 64)
		}
	}
	khronos := uint32(ticket / 1000)
	sum := sm3Sum([]byte(query))
	bodyMD5 := make([]byte, 16)
	if body != nil {
		s := md5Raw(body)
		bodyMD5 = s[:]
	}
	return branchOf(sum[:], bodyMD5, le32(khronos))
}

// SignPOSTBranch signs like SignPOST, advancing the base ticket in
// 33 ms steps until the signed query lands on the wanted medusa branch.
// Reference clients were observed (2026-11-10) to only send branch-0
// playback requests; the default signer accepts branches 0 and 2.
func SignPOSTBranch(path string, pairs [][2]string, nowMillis int64, deviceID, versionName string, body []byte, entropy *Entropy, want uint32) (string, map[string]string) {
	for i := 0; i < 256; i++ {
		q, h := SignPOST(path, pairs, nowMillis+int64(i*33), deviceID, versionName, body, entropy)
		if BranchOfSigned(q, body) == want {
			return q, h
		}
	}
	return SignPOST(path, pairs, nowMillis, deviceID, versionName, body, entropy)
}

// SignGETBranch is SignPOSTBranch for a bodyless GET.
func SignGETBranch(pairs [][2]string, nowMillis int64, deviceID, versionName string, entropy *Entropy, want uint32) (string, map[string]string) {
	for i := 0; i < 256; i++ {
		q, h := SignGET(pairs, nowMillis+int64(i*33), deviceID, versionName, entropy)
		if BranchOfSigned(q, nil) == want {
			return q, h
		}
	}
	return SignGET(pairs, nowMillis, deviceID, versionName, entropy)
}
