package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/beilunyang/openhongguo/internal/protocol"
	"github.com/beilunyang/openhongguo/sdk/model"
)

// 播放取址的计算方式（签名来源）。本地计算是默认与根基；两种远程计算
// 是 hongguoTV 公开客户端内置的第三方签名服务，本包按其公开协议接入：
// unidbg 服务只算签名、身份仍是本机目录游客；frida 服务自带一台真机
// 身份，身份与签名都由服务提供。两种远程方式都走 video_tab 单集取址
// （服务方客户端的实测结论：该通道对匿名设备最宽容）。
const (
	SigningModeLocal  = 0
	SigningModeUnidbg = 1
	SigningModeFrida  = 2
)

const (
	unidbgSignerEndpoint = "http://taihe.678120.cn:8028"
	fridaSignerEndpoint  = "http://taihe.678120.cn:8056"
)

// signatureHeaderNames 是签名服务必须返回的六个签名头（小写名）。
var signatureHeaderNames = []string{
	"x-gorgon", "x-khronos", "x-argus", "x-ladon", "x-medusa", "x-helios",
}

// SetSigningMode 切换播放取址的计算方式："local"（默认）/ "unidbg" /
// "frida"；未知取值按本地计算处理。只影响播放取址（单集与批量），
// 目录、搜索、评论等读取不变。
func (c *Client) SetSigningMode(mode string) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "unidbg":
		c.signMode.Store(SigningModeUnidbg)
	case "frida":
		c.signMode.Store(SigningModeFrida)
	default:
		c.signMode.Store(SigningModeLocal)
	}
}

func (c *Client) signingMode() int32 { return c.signMode.Load() }

// resolvePlaybackViaRemote 按当前计算方式走远程签名取址；本地方式或
// 远程未取到模型时返回 (nil, nil)，错误只在远程链路本身失败时返回，
// 供 ResolvePlayback 在本地链也失败时把远程失败原因报给用户。
func (c *Client) resolvePlaybackViaRemote(ctx context.Context, episodeID string) (*model.Playback, error) {
	switch c.signingMode() {
	case SigningModeUnidbg:
		return c.resolvePlaybackViaUnidbg(ctx, episodeID)
	case SigningModeFrida:
		return c.resolvePlaybackViaFrida(ctx, episodeID)
	}
	return nil, nil
}

// parseTabPlayback 解析 video_tab 单集响应（与本地 tab 通道同一信封）。
func parseTabPlayback(raw []byte, episodeID string) (*model.Playback, error) {
	playback, err := protocol.ParsePlayback(raw, episodeID)
	if err != nil {
		return nil, err
	}
	if playback.Protected {
		return playback, ErrDRMProtected
	}
	return playback, nil
}

// resolvePlaybackViaUnidbg：本机身份 + 远程算签名。取址 URL 与本地
// tab 通道逐字一致（SignedVideoTabPairs + ts/_rticket），只是六个
// 签名头不由本机算法生成，而是把完整 URL 交给 unidbg 服务计算。
func (c *Client) resolvePlaybackViaUnidbg(ctx context.Context, episodeID string) (*model.Playback, error) {
	p, err := c.ensureSearchGuest(ctx)
	if err != nil {
		return nil, err
	}
	ticket := c.clock().UnixMilli()
	pairs := append(protocol.SignedVideoTabPairs(c.identity(p), episodeID),
		[2]string{"ts", strconv.FormatInt(ticket/1000, 10)},
		[2]string{"_rticket", strconv.FormatInt(ticket, 10)})
	fullURL := c.apiOrigin + protocol.PathVideoTabModel + "?" + encodeRemoteQuery(pairs)
	sigs, err := c.unidbgSign(ctx, fullURL)
	if err != nil {
		return nil, err
	}
	raw, _, err := c.doRaw(ctx, "GET", fullURL, anonymousHeaders(c, p, ticket, sigs), nil)
	if err != nil {
		return nil, err
	}
	return parseTabPlayback(raw, episodeID)
}

// unidbgSign 调用 openhongguo-signer 的 POST /feature-hash：
// 请求 {"url": 完整待签 URL}，返回 {"headers": {六个签名头}}。
func (c *Client) unidbgSign(ctx context.Context, fullURL string) (map[string]string, error) {
	payload, _ := json.Marshal(map[string]string{"url": fullURL})
	root, err := c.signerServiceJSON(ctx, "POST", unidbgSignerEndpoint+"/feature-hash", payload, "远程计算-unidbg")
	if err != nil {
		return nil, err
	}
	headersObj, ok := root["headers"].(map[string]any)
	if !ok {
		return nil, errors.New("远程计算-unidbg 未返回签名头")
	}
	return extractSignatureHeaders(headersObj, "远程计算-unidbg")
}

// fridaServiceProfile 是 frida 服务自带的设备档案（GET /info）。
type fridaServiceProfile struct {
	params              map[string]string
	userAgent           string
	supportsBodySigning bool
}

// fridaIdentityKeys 是服务方客户端从 /info 档案里采纳的身份字段全集，
// 与其 IDENTITY_KEYS 一致：档案里的其他字段不进查询串。
var fridaIdentityKeys = []string{
	"device_id", "iid", "cdid", "openudid", "ac", "channel", "aid", "app_name",
	"version_code", "version_name", "manifest_version_code", "update_version_code",
	"pv_player", "device_platform", "os", "os_api", "os_version", "ssmix",
	"device_type", "device_brand", "language", "resolution", "dpi", "host_abi",
	"dragon_device_type", "user_is_login", "compliance_status",
	"need_personal_recommend", "player_so_load", "is_android_pad_screen",
}

// fridaProfileCached 拉取并缓存 frida 服务的设备档案（60 秒）。
func (c *Client) fridaProfileCached(ctx context.Context) (*fridaServiceProfile, error) {
	c.fridaMu.Lock()
	defer c.fridaMu.Unlock()
	if c.fridaProfile != nil && time.Since(c.fridaAt) < 60*time.Second {
		return c.fridaProfile, nil
	}
	root, err := c.signerServiceJSON(ctx, "GET", fridaSignerEndpoint+"/info", nil, "远程计算-frida")
	if err != nil {
		return nil, err
	}
	dp, ok := root["deviceProfile"].(map[string]any)
	if !ok {
		return nil, errors.New("远程计算-frida 未提供设备资料")
	}
	paramsObj, ok := dp["params"].(map[string]any)
	if !ok {
		return nil, errors.New("远程计算-frida 的设备资料不完整")
	}
	params := make(map[string]string, len(paramsObj))
	for k, v := range paramsObj {
		if s, ok := v.(string); ok && s != "" {
			params[k] = s
		}
	}
	ua, _ := dp["user_agent"].(string)
	if ua == "" {
		ua, _ = dp["userAgent"].(string)
	}
	if params["device_id"] == "" || params["iid"] == "" || params["cdid"] == "" || ua == "" {
		return nil, errors.New("远程计算-frida 中的设备尚未就绪")
	}
	prof := &fridaServiceProfile{params: params, userAgent: ua}
	prof.supportsBodySigning, _ = root["supportsBodySigning"].(bool)
	c.fridaProfile = prof
	c.fridaAt = time.Now()
	return prof, nil
}

// resolvePlaybackViaFrida：身份与签名都来自 frida 服务。查询串的合并
// 规则与服务方客户端一致：业务参数在前，身份字段覆盖同名键，最后
// 追加 ts 与 _rticket；签好的六头与服务档案的 UA 一起发出。
func (c *Client) resolvePlaybackViaFrida(ctx context.Context, episodeID string) (*model.Playback, error) {
	prof, err := c.fridaProfileCached(ctx)
	if err != nil {
		return nil, err
	}
	ticket := c.clock().UnixMilli()
	var pairs [][2]string
	seen := make(map[string]bool)
	add := func(k, v string) {
		if !seen[k] {
			seen[k] = true
			pairs = append(pairs, [2]string{k, v})
		}
	}
	add("video_id", episodeID)
	add("source", "0")
	add("need_all_video_definition", "true")
	for _, k := range fridaIdentityKeys {
		if v, ok := prof.params[k]; ok {
			add(k, v)
		}
	}
	for k, v := range prof.params {
		add(k, v)
	}
	add("ts", strconv.FormatInt(ticket/1000, 10))
	add("_rticket", strconv.FormatInt(ticket, 10))
	fullURL := c.apiOrigin + protocol.PathVideoTabModel + "?" + encodeRemoteQuery(pairs)
	sigs, err := c.fridaSign(ctx, fullURL)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{
		"Accept":                    "application/json; charset=utf-8,application/x-protobuf",
		"User-Agent":                prof.userAgent,
		"x-xs-from-web":             "0",
		"x-ss-req-ticket":           strconv.FormatInt(ticket, 10),
		"x-tt-request-tag":          "t=0;n=0",
		"sdk-version":               "2",
		"passport-sdk-version":      "50561",
		"x-vc-bdturing-sdk-version": "3.7.2.cn",
	}
	for k, v := range sigs {
		headers[k] = v
	}
	raw, _, err := c.doRaw(ctx, "GET", fullURL, headers, nil)
	if err != nil {
		return nil, err
	}
	return parseTabPlayback(raw, episodeID)
}

// fridaSign 调用 frida 服务的 POST /sign：请求 {"url": 完整待签 URL}，
// 六个签名头平铺在响应顶层返回。
func (c *Client) fridaSign(ctx context.Context, fullURL string) (map[string]string, error) {
	payload, _ := json.Marshal(map[string]string{"url": fullURL})
	root, err := c.signerServiceJSON(ctx, "POST", fridaSignerEndpoint+"/sign", payload, "远程计算-frida")
	if err != nil {
		return nil, err
	}
	return extractSignatureHeaders(root, "远程计算-frida")
}

// signerServiceJSON 与签名服务做一次 JSON 往返（45 秒超时，独立于
// 平台请求的重试逻辑）。错误文案带方式名，用户可据此切换计算方式。
func (c *Client) signerServiceJSON(ctx context.Context, method, url string, payload []byte, label string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	headers := map[string]string{"Accept": "application/json"}
	if payload != nil {
		headers["Content-Type"] = "application/json; charset=utf-8"
	}
	resp, err := c.transport.Do(ctx, &Request{Method: method, URL: url, Headers: headers, Body: payload})
	if err != nil {
		return nil, fmt.Errorf("%s服务未连接", label)
	}
	body, err := decodeBody(resp)
	if err != nil {
		return nil, fmt.Errorf("%s服务返回异常", label)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var m map[string]any
		if json.Unmarshal(body, &m) == nil {
			if e, _ := m["error"].(string); e != "" {
				return nil, fmt.Errorf("%s服务返回错误：%s", label, e)
			}
		}
		return nil, fmt.Errorf("%s服务不可用（HTTP %d）", label, resp.StatusCode)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, fmt.Errorf("%s服务返回空响应", label)
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("%s服务返回格式异常", label)
	}
	return root, nil
}

// extractSignatureHeaders 从服务响应里摘出六个签名头（键名大小写
// 不敏感）；缺任何一个都算失败——半套签名发出去只会换来空响应。
func extractSignatureHeaders(obj map[string]any, label string) (map[string]string, error) {
	out := make(map[string]string, len(signatureHeaderNames))
	for k, v := range obj {
		name := strings.ToLower(k)
		for _, want := range signatureHeaderNames {
			if name == want {
				if s, ok := v.(string); ok && s != "" {
					out[name] = s
				}
			}
		}
	}
	for _, want := range signatureHeaderNames {
		if out[want] == "" {
			return nil, fmt.Errorf("%s未返回完整签名", label)
		}
	}
	return out, nil
}

// encodeRemoteQuery 把参数对编码成查询串，编码口径与本地签名包的
// encodeQuery 一致（同一组不转义字符），保证远程签名的 URL 与本地
// 通道发出的 URL 逐字同形。
func encodeRemoteQuery(pairs [][2]string) string {
	parts := make([]string, len(pairs))
	for i, pair := range pairs {
		parts[i] = encodeRemoteComponent(pair[0]) + "=" + encodeRemoteComponent(pair[1])
	}
	return strings.Join(parts, "&")
}

func encodeRemoteComponent(s string) string {
	const unreserved = "-_.!~*'()"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			strings.ContainsRune(unreserved, rune(c)) {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}
