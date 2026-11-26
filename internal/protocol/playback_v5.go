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

// PlaybackV041003Pairs 是 v04.10.03 档 player/v1 通道的查询画像
// （7.4.5.32/74532），顺序与构成按 2026-11-25 实测可达业务层的
// 构造：_rticket 与 ts 是空占位，由 SignPlaybackV5InPlace 就地填充；
// 与 PlaybackV5Pairs（7.4.3.32 档）相比无 pv_player 等附加字段、
// 带 rom_version，device_id/iid 在尾部。
func PlaybackV041003Pairs(id SearchIdentity) [][2]string {
	d := id.Device.OrDefault()
	kind, pad := "phone", "0"
	if d.Pad {
		kind, pad = "pad", "1"
	}
	version := PlaybackV041003VersionCode
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
		{"version_name", PlaybackV041003VersionName},
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
		{"is_pad", pad},
		{"rom_version", d.Build},
		{"device_id", id.DeviceID},
		{"iid", id.InstallID},
	}
}

// PlaybackV041003Body 是 v04.10.03 档的明文 JSON body（device_level 1、
// 不带 dr_scene）；调用方 gzip 后把同一份字节同时用于签名和发送。
func PlaybackV041003Body(episodeID string) ([]byte, error) {
	return jsonMarshal(map[string]any{
		"biz_param": map[string]any{
			"detail_page_version": 0, "device_level": 1,
			"disable_video_relate_book": false, "need_all_video_definition": true,
			"need_mp4_align": false, "use_os_player": false, "use_server_dns": false,
			"video_platform": 1024,
		},
		"mixed_video_id_map": map[string][]string{"1004": {episodeID}},
	})
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

// ---- 稀疏变体（video_model/v1 直打，hongguoTV PlaybackV5Signer 形状） ----

// PlaybackV5SparseUserAgent 是稀疏变体通道的固定 UA：画像版本写
// 73932、机型与查询模板一致（25053RT47C / Android 16），与查询串里
// 的 74532 画像不一致是该实现原样，不要对齐。
const PlaybackV5SparseUserAgent = "com.phoenix.read/73932 (Linux; U; Android 16; zh_CN; 25053RT47C; Build/BP2A.250605.031.A3; Cronet/TTNetVersion:04657795 2026-01-23 QuicVersion:c67e9834 2025-09-08)"

// PlaybackV5SparsePairs 是稀疏变体的查询模板（74532 画像，顺序即
// 线序）：机型/品牌/系统/渠道是固定模板值，只有 iid / device_id /
// cdid 来自客户端身份。签名器在其后追加 _rticket；本模板不带 ts。
func PlaybackV5SparsePairs(deviceID, installID, cdid string) [][2]string {
	return [][2]string{
		{"aid", AID},
		{"app_name", AppName},
		{"version_code", PlaybackV041003VersionCode},
		{"version_name", PlaybackV041003VersionName},
		{"update_version_code", PlaybackV041003VersionCode},
		{"manifest_version_code", PlaybackV041003VersionCode},
		{"os", "android"},
		{"ssmix", "a"},
		{"channel", "update_64"},
		{"device_platform", DevicePlatform},
		{"device_type", "25053RT47C"},
		{"device_brand", "Redmi"},
		{"os_version", "16"},
		{"os_api", "36"},
		{"ac", "wifi"},
		{"language", "zh"},
		{"host_abi", "arm64-v8a"},
		{"iid", installID},
		{"device_id", deviceID},
		{"cdid", cdid},
	}
}

// PlaybackVideoModelBody 是 video_model/v1 直打的明文 JSON body：
// biz_param 只带取全档必需字段 + content_type/video_id。调用方
// gzip 后把同一份字节同时用于签名和发送。2026-11-25 实测：扁平
// 体（无 biz_param）只回单档 720p，本形状回全 5 档。
func PlaybackVideoModelBody(episodeID string) ([]byte, error) {
	return PlaybackVideoModelBodyFor(episodeID, 1)
}

// PlaybackVideoModelBodyFor 是带 content_type 的版本：上游对部分类型
// （PUGC 1001）要求请求 content_type 与单集真实类型一致，写死 1 会被
// 回 101000「服务异常」（2026-11-26 实测《娇女诡爱畸恋》：ct=1 全灭、
// ct=1001 五档全回）。contentType <= 0 时回落 1（真人短剧）。
func PlaybackVideoModelBodyFor(episodeID string, contentType int) ([]byte, error) {
	if contentType <= 0 {
		contentType = 1
	}
	return jsonMarshal(map[string]any{
		"biz_param": map[string]any{
			"need_all_video_definition": true,
			"use_os_player":               false,
			"video_platform":              1024,
		},
		"content_type": contentType,
		"video_id":     episodeID,
	})
}
