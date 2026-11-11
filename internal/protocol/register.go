package protocol

import (
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/beilunyang/openhongguo/sdk/model"
)

// RegisterResult is the non-secret part of a guest registration response.
type RegisterResult struct {
	DeviceID  string
	InstallID string
	NewUser   int
	// DeviceToken is the registration-issued device token the client
	// presents as x-tt-dt on signed requests (bdinstall's request
	// header). It stays in memory/profile only, never in logs.
	DeviceToken string
}

// BuildRegisterBody gzip-compresses the anonymous ss_app_log envelope and applies
// the portable tt-data wrapper. It does not attach an account token.
func BuildRegisterBody(profile RegisterIdentity, generatedAt time.Time) ([]byte, error) {
	plain, err := BuildBootstrapPlaintext(registerHeader(profile, generatedAt), generatedAt)
	if err != nil {
		return nil, err
	}
	var gz bytes.Buffer
	w, err := gzip.NewWriterLevel(&gz, gzip.DefaultCompression)
	if err != nil {
		return nil, err
	}
	w.Header.ModTime = generatedAt.UTC()
	w.Header.OS = 255
	if _, err := w.Write(plain); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return TTEncrypt(gz.Bytes()), nil
}

// RegisterIdentity is the locally generated half of a guest registration.
type RegisterIdentity struct {
	CDID       string
	OpenUDID   string
	ClientUDID string
	OAID       string
	ReqID      string
	// Device is the phone the registration describes, the same one every
	// later request's query and User-Agent name.
	Device model.Device
}

func registerHeader(p RegisterIdentity, now time.Time) map[string]any {
	d := p.Device.OrDefault()
	return map[string]any{
		"display_name":          "红果免费短剧",
		"update_version_code":   74516,
		"manifest_version_code": 74516,
		"app_version_minor":     VersionName,
		"aid":                   8662,
		"channel":               d.Channel,
		"package":               Package,
		"app_version":           VersionName,
		"version_code":          74516,
		"os":                    "Android",
		"os_version":            d.OSVersion,
		"os_api":                d.OSAPI,
		"device_model":          d.Model,
		"device_brand":          d.Brand,
		"device_manufacturer":   d.Brand,
		"cpu_abi":               "arm64-v8a",
		"language":              "zh",
		"timezone":              8,
		"access":                "wifi",
		"region":                "CN",
		"tz_name":               "Asia/Shanghai",
		"tz_offset":             28800,
		"cdid":                  p.CDID,
		"openudid":              p.OpenUDID,
		"clientudid":            p.ClientUDID,
		"ipv6_list":             []any{},
		"sim_serial_number":     []any{},
		"oaid":                  BuildBootstrapOAID(p.OAID, now.Add(-200*time.Millisecond)),
		"oaid_may_support":      true,
		"req_id":                p.ReqID,
		"device_platform":       DevicePlatform,
		"guest_mode":            0,
		"is_system_app":         0,
		"sdk_flavor":            "china",
		"device_id":             "0",
		"install_id":            "0",
	}
}

// BuildBootstrapOAID is the object-valued OAID field. The id is caller-generated.
func BuildBootstrapOAID(id string, issuedAt time.Time) map[string]any {
	return map[string]any{
		"req_id":             id,
		"hw_id_version_code": "0",
		"take_ms":            "6",
		"is_track_limited":   "false",
		"query_times":        "1",
		"id":                 id,
		"time":               strconv.FormatInt(issuedAt.UnixMilli(), 10),
	}
}

var bootstrapHeaderOrder = []string{
	"display_name", "update_version_code", "manifest_version_code", "app_version_minor",
	"aid", "channel", "package", "app_version", "version_code", "os", "os_version", "os_api",
	"device_model", "device_brand", "device_manufacturer", "cpu_abi", "language", "timezone",
	"access", "region", "tz_name", "tz_offset", "cdid", "openudid", "clientudid", "ipv6_list",
	"sim_serial_number", "oaid", "oaid_may_support", "req_id", "device_platform", "guest_mode",
	"is_system_app", "sdk_flavor", "device_id", "install_id",
}

// playbackRegisterHeaderOrder is the field order of the playback
// registration header: the 7.3.9.32-generation registration shape the
// multi playback channels' identities are registered with.
var playbackRegisterHeaderOrder = []string{
	"display_name", "update_version_code", "manifest_version_code", "app_version_minor",
	"aid", "channel", "package", "app_version", "version_code", "sdk_version",
	"sdk_target_version", "git_hash", "os", "os_version", "os_api", "device_model",
	"device_brand", "device_manufacturer", "cpu_abi", "release_build", "density_dpi",
	"display_density", "resolution", "language", "timezone", "access", "not_request_sender",
	"carrier", "mcc_mnc", "rom", "rom_version", "cdid", "sig_hash", "openudid",
	"clientudid", "ipv6_list", "region", "tz_name", "tz_offset", "sim_region",
	"sim_serial_number", "oaid_may_support", "req_id", "device_platform", "custom",
	"pre_installed_channel", "apk_first_install_time", "is_system_app", "sdk_flavor",
	"guest_mode",
}

// BuildPlaybackRegisterBody builds the registration body for a
// playback identity: the same tt-data envelope as BuildRegisterBody,
// but with the registration header the current playback client
// generation sends (7.3.9.32 version fields, the Redmi playback
// device, carrier/ROM/build fingerprints, no OAID object). The
// identity's random ids (cdid, openudid, clientudid, req_id) come
// from the caller, exactly as with BuildRegisterBody.
func BuildPlaybackRegisterBody(p RegisterIdentity, now time.Time) ([]byte, error) {
	d := p.Device.OrDefault()
	header := map[string]any{
		"display_name":          "红果免费短剧",
		"update_version_code":   73932,
		"manifest_version_code": 73932,
		"app_version_minor":     "7.3.9.32",
		"aid":                   8662,
		"channel":               d.Channel,
		"package":               Package,
		"app_version":           "7.3.9.32",
		"version_code":          73932,
		"sdk_version":           "3.7.0-rc.34-fanqie-xiaoshuo",
		"sdk_target_version":    29,
		"git_hash":              "3a88f4e",
		"os":                    "Android",
		"os_version":            d.OSVersion,
		"os_api":                d.OSAPI,
		"device_model":          d.Model,
		"device_brand":          d.Brand,
		"device_manufacturer":   d.Brand,
		"cpu_abi":               "arm64-v8a",
		"release_build":         "b5e187f",
		"density_dpi":           d.DPI,
		"display_density":       "mdpi",
		"resolution":            strings.Replace(d.Resolution, "*", "x", 1),
		"language":              "zh",
		"timezone":              8,
		"access":                "wifi",
		"not_request_sender":    0,
		"carrier":               "CHINA MOBILE",
		"mcc_mnc":               "46000",
		"rom":                   "eng.root.20231116.160340",
		"rom_version":           "W528JS release-keys",
		"cdid":                  p.CDID,
		"sig_hash":              "56a962410c494bbaf0b58dba20cae56f",
		"openudid":              p.OpenUDID,
		"clientudid":            p.ClientUDID,
		"ipv6_list":             []any{},
		"region":                "CN",
		"tz_name":               "Asia/Shanghai",
		"tz_offset":             28800,
		"sim_region":            "cn",
		"sim_serial_number":     []any{},
		"oaid_may_support":      false,
		"req_id":                p.ReqID,
		"device_platform":       DevicePlatform,
		"custom": map[string]any{
			"account_region_src": "fallback",
			"account_region":     "cn",
			"host_bit":           64,
			"dragon_device_type": "phone",
		},
		"pre_installed_channel":  "",
		"apk_first_install_time": now.UnixMilli(),
		"is_system_app":          0,
		"sdk_flavor":             "china",
		"guest_mode":             0,
	}
	headerJSON, err := marshalOrdered(header, playbackRegisterHeaderOrder)
	if err != nil {
		return nil, err
	}
	var payload bytes.Buffer
	payload.WriteString(`{"magic_tag":"ss_app_log","header":`)
	payload.Write(headerJSON)
	payload.WriteString(`,"_gen_time":`)
	genTime, _ := json.Marshal(now.UnixMilli())
	payload.Write(genTime)
	payload.WriteByte('}')
	var gz bytes.Buffer
	w, err := gzip.NewWriterLevel(&gz, gzip.DefaultCompression)
	if err != nil {
		return nil, err
	}
	w.Header.OS = 3
	if _, err := w.Write(payload.Bytes()); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return TTEncrypt(gz.Bytes()), nil
}

// BuildBootstrapPlaintext is the gzip input: magic_tag ss_app_log plus header.
func BuildBootstrapPlaintext(header map[string]any, generatedAt time.Time) ([]byte, error) {
	copyHeader := make(map[string]any, len(header)+2)
	for name, value := range header {
		copyHeader[name] = value
	}
	if copyHeader["ipv6_list"] == nil {
		copyHeader["ipv6_list"] = []any{}
	}
	if copyHeader["sim_serial_number"] == nil {
		copyHeader["sim_serial_number"] = []any{}
	}
	headerJSON, err := marshalOrdered(copyHeader, bootstrapHeaderOrder)
	if err != nil {
		return nil, err
	}
	var payload bytes.Buffer
	payload.WriteString(`{"magic_tag":"ss_app_log","header":`)
	payload.Write(headerJSON)
	payload.WriteString(`,"_gen_time":`)
	genTime, _ := json.Marshal(generatedAt.UnixMilli())
	payload.Write(genTime)
	payload.WriteByte('}')
	return payload.Bytes(), nil
}

func marshalOrdered(values map[string]any, preferred []string) ([]byte, error) {
	keys := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, key := range preferred {
		if _, ok := values[key]; ok {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	rest := make([]string, 0)
	for key := range values {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	keys = append(keys, rest...)
	var out bytes.Buffer
	out.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			out.WriteByte(',')
		}
		kb, _ := json.Marshal(key)
		out.Write(kb)
		out.WriteByte(':')
		vb, err := json.Marshal(values[key])
		if err != nil {
			return nil, err
		}
		out.Write(vb)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

// ContentMD5 is the portable X-SS-STUB value: uppercase MD5 of the body.
// Callers send it as a header. Tests must not persist the value.
func ContentMD5(body []byte) string {
	sum := md5.Sum(body)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// ParseRegister reads a guest registration response.
// A missing or zero id is not a successful anonymous registration.
// device_token is returned for the x-tt-dt header; it is not required.
func ParseRegister(raw []byte) (RegisterResult, error) {
	var env struct {
		DeviceID    flexString `json:"device_id_str"`
		InstallID   flexString `json:"install_id_str"`
		NewUser     int        `json:"new_user"`
		DeviceToken string     `json:"device_token"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return RegisterResult{}, err
	}
	id := env.DeviceID.String()
	install := env.InstallID.String()
	if id == "" || id == "0" || install == "" || install == "0" {
		return RegisterResult{}, errNoGuest
	}
	return RegisterResult{DeviceID: id, InstallID: install, NewUser: env.NewUser, DeviceToken: env.DeviceToken}, nil
}

type noGuestError struct{}

func (noGuestError) Error() string { return "anonymous registration returned no guest identity" }

var errNoGuest = noGuestError{}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
