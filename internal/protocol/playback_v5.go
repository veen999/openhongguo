package protocol

import (
	"fmt"
	"strconv"

	"github.com/beilunyang/openhongguo/sdk/model"
)

const (
	// PathPlaybackV5 是当前服务端接受的播放取模路由（旧版签名档走
	// multi_video_model/v1/；player/v1 路由已不再下发模型）。
	PathPlaybackV5 = "/novel/player/multi_video_model/v1/"
)

// PlaybackUserAgent is the Cronet User-Agent the 7.4.3.32 client sends with
// the V5 playback request: UserAgent's format with the playback version.
// 版本常量用 app.go 的 PlaybackVersionCode（字符串，与本 fork 既有
// 声明统一，避免重复声明）。
func PlaybackUserAgent(d model.Device) string {
	d = d.OrDefault()
	return fmt.Sprintf("%s/%s (Linux; U; Android %s; zh_CN; %s; Build/%s; Cronet/TTNetVersion:8d40f833 2026-03-03 QuicVersion:21ac1950 2025-11-18)",
		Package, PlaybackVersionCode, d.OSVersion, d.Model, d.Build)
}

// PlaybackV5Pairs is the V5 playback query in the order the 7.4.3.32 client
// sends it (captured 2026-11-18), with the profile's device and anonymous
// install. The empty _rticket marks where the signer puts the ticket; order
// is part of the signature.
func PlaybackV5Pairs(id SearchIdentity) [][2]string {
	d := id.Device.OrDefault()
	kind, pad := "phone", "0"
	if d.Pad {
		kind, pad = "pad", "1"
	}
	version := PlaybackVersionCode
	return [][2]string{
		{"device_platform", DevicePlatform},
		{"os", "android"},
		{"ssmix", "a"},
		{"_rticket", ""},
		{"ts", ""},
		{"cdid", id.CDID},
		{"channel", d.Channel},
		{"aid", AID},
		{"app_name", AppName},
		{"version_code", version},
		{"version_name", PlaybackVersionName},
		{"manifest_version_code", version},
		{"update_version_code", version},
		{"resolution", d.Resolution},
		{"dpi", strconv.Itoa(d.DPI)},
		{"device_type", d.Model},
		{"device_brand", d.Brand},
		{"language", "zh"},
		{"os_api", strconv.Itoa(d.OSAPI)},
		{"os_version", d.OSVersion},
		{"ac", "wifi"},
		{"host_abi", "arm64-v8a"},
		{"dragon_device_type", kind},
		{"pv_player", version},
		{"compliance_status", "0"},
		{"need_personal_recommend", "1"},
		{"player_so_load", "1"},
		{"is_android_pad_screen", pad},
		{"iid", id.InstallID},
		{"device_id", id.DeviceID},
	}
}

// PlaybackV5Body 返回明文 JSON；调用方把同一份字节同时用于签名和发送。
//
// device_level 与旧版播放协议一致用 4；此档下发的全是 bytevc1，ffmpeg/mpv 可解。
func PlaybackV5Body(episodeID string) ([]byte, error) {
	return jsonMarshal(map[string]any{
		"biz_param": map[string]any{
			"detail_page_version": 0, "device_level": 4, "disable_digg_stat": false,
			"disable_video_relate_book": false, "need_all_video_definition": true,
			"need_mp4_align": false, "use_os_player": false, "use_server_dns": false, "video_platform": 1024,
		},
		"mixed_video_id_map": map[string][]string{"1004": {episodeID}},
	})
}
