package protocol

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// Novel content-key chain (reverse-engineered 2026-11-06, probe-books).
//
// Chapter text from /reading/reader/full/v is AES-128-CBC encrypted under a
// per-identity content key. The key is established once per guest identity:
// the client encrypts a seed of (deviceId, userId) with a fixed key baked
// into the official client and POSTs it to /reading/crypt/registerkey; the
// server binds the content key to that identity and returns it wrapped
// under the same fixed key. userId is 0 for guests.
//
// The seed segments are LITTLE-endian per segment (big-endian is rejected
// with code 500002 "verify fail").

// contentKeyFixedKeyHex is the fixed AES key the official client embeds
// for the registerkey wrap/unwrap.
const contentKeyFixedKeyHex = "ac25c67ddd8f38c1b37a2348828e222e"

func pkcs5Pad(b []byte) []byte {
	n := aes.BlockSize - len(b)%aes.BlockSize
	return append(b, bytes.Repeat([]byte{byte(n)}, n)...)
}

func pkcs5Unpad(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return b, nil
	}
	n := int(b[len(b)-1])
	if n == 0 || n > aes.BlockSize || n > len(b) {
		return nil, fmt.Errorf("invalid pkcs5 padding")
	}
	return b[:len(b)-n], nil
}

func aesCBC(key, iv, data []byte, encrypt bool) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(iv) != aes.BlockSize {
		return nil, fmt.Errorf("iv length %d", len(iv))
	}
	if len(data)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("data length %d not a block multiple", len(data))
	}
	out := make([]byte, len(data))
	if encrypt {
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, data)
	} else {
		cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	}
	return out, nil
}

func pad16(s string) []byte {
	b := []byte(s)
	for len(b) < 16 {
		b = append(b, '0')
	}
	return b[:16]
}

// BuildRegisterKeyContent builds the registerkey request's content field
// for a guest identity: base64( ivStr || AES-CBC(seed) ), where ivStr is a
// fresh 16-hex-char string and seed = LE64(deviceID) || LE64(0).
func BuildRegisterKeyContent(deviceID int64) (string, error) {
	seed := make([]byte, 16)
	binary.LittleEndian.PutUint64(seed[0:8], uint64(deviceID))
	binary.LittleEndian.PutUint64(seed[8:16], 0)
	ivRaw := make([]byte, 8)
	if _, err := rand.Read(ivRaw); err != nil {
		return "", err
	}
	ivStr := hex.EncodeToString(ivRaw)
	fixedKey, err := hex.DecodeString(contentKeyFixedKeyHex)
	if err != nil {
		return "", err
	}
	ct, err := aesCBC(fixedKey, pad16(ivStr), pkcs5Pad(seed), true)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(append([]byte(ivStr), ct...)), nil
}

// decryptWrapped is the shared unwrap: base64 -> iv is the first 16 bytes,
// AES-CBC decrypt the rest under keyHex, strip PKCS5 padding.
func decryptWrapped(b64, keyHex string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, err
	}
	if len(raw) <= 16 {
		return nil, fmt.Errorf("wrapped value too short")
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, err
	}
	plain, err := aesCBC(key, raw[:16], raw[16:], false)
	if err != nil {
		return nil, err
	}
	return pkcs5Unpad(plain)
}

// DeriveContentKey unwraps the registerkey response's key field into the
// identity's content key, as an uppercase hex string (32 chars).
func DeriveContentKey(keyB64 string) (string, error) {
	plain, err := decryptWrapped(keyB64, contentKeyFixedKeyHex)
	if err != nil {
		return "", err
	}
	if len(plain) != 16 {
		return "", fmt.Errorf("content key length %d", len(plain))
	}
	return strings.ToUpper(hex.EncodeToString(plain)), nil
}

// DecryptChapterContent decrypts one chapter's content field under the
// identity content key and gunzips it, returning the chapter HTML.
func DecryptChapterContent(contentB64, contentKeyHex string) (string, error) {
	plain, err := decryptWrapped(contentB64, contentKeyHex)
	if err != nil {
		return "", err
	}
	r, err := gzip.NewReader(bytes.NewReader(plain))
	if err != nil {
		return "", fmt.Errorf("chapter payload is not gzip: %w", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
