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
// 是公开客户端内置的第三方签名服务，本包按其公开协议接入：
// unidbg 服务只算签名、身份仍是本机身份——接入形态与上游 openhongguo
// v0.4.0 的 SignerURL 机制一致：本地链照常构造播放请求，发出前由传输
// 层把签名头整体换成服务算出的签名（见 resign_transport.go）；
// frida 服务自带一台真机身份，身份与签名都由服务提供，走 video_tab
// 单集取址（见 resolvePlaybackViaFrida）。
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
// 目录、搜索、评论等读取不变。重复下发同一选择时直接返回——设置流
// 每次发状态都会调用本方法，无变化时不得重置策略层的自动切换状态。
func (c *Client) SetSigningMode(mode string) {
	var next int32 = SigningModeLocal
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "unidbg":
		next = SigningModeUnidbg
	case "frida":
		next = SigningModeFrida
	}
	if c.signMode.Load() == next {
		return
	}
	c.signMode.Store(next)
	c.effSignMode.Store(next)
	c.localFailCount.Store(0)
}

func (c *Client) signingMode() int32 { return c.signMode.Load() }

func (c *Client) effectiveSigningMode() int32 { return c.effSignMode.Load() }

// EffectiveSigningMode 返回算法策略层当前实际生效的计算方式
// （"local" / "unidbg" / "frida"）。它可能与用户在设置里选的不同：
// 本地连续取址失败会自动切到 unidbg，本地恢复后会自动切回。
func (c *Client) EffectiveSigningMode() string {
	switch c.effectiveSigningMode() {
	case SigningModeUnidbg:
		return "unidbg"
	case SigningModeFrida:
		return "frida"
	}
	return "local"
}

// SetSigningModeAuto 是设置同步专用入口：App 把策略层自动切换的结果
// 回写为持久化设置后，用本方法把实际生效方式镜像回 SDK（如重启后
// 恢复）。与 SetSigningMode 的区别是它不改动用户选择锚点、不清零
// 失败计数——锚点仍是用户显式选的方式，本地恢复探测因此继续工作。
// 锚点不是本地方式时（用户已手动改选）直接忽略。
func (c *Client) SetSigningModeAuto(mode string) {
	if c.signingMode() != SigningModeLocal {
		return
	}
	var next int32 = SigningModeLocal
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "unidbg":
		next = SigningModeUnidbg
	case "frida":
		next = SigningModeFrida
	}
	if next == SigningModeFrida {
		return // 自动切换只在本地与 unidbg 之间发生
	}
	c.effSignMode.Store(next)
}

// noteLocalFailure 处理一次本地全链取址失败，决定要不要切到远程
// 计算-unidbg。用户规则（2026-11-23 定版，取代 2026-11-20 案二的
// 「失败 1 次即切」）：先探测完本地多个通道是否可用，都不可用再换
// 远程——全链失败一次不等于本地全灭（新播放身份在平台试用期内会
// 整片空返、通道也会逐个开关），必须经多通道复核确认。复核发现任
// 一本地通道可用 → 返回 false（不切，调用方按本地再跑一遍全链）；
// 全部确认不可用 → 把实际生效方式切到 unidbg 并返回 true，调用方
// 用新方式重试本次取址。
func (c *Client) noteLocalFailure(ctx context.Context, episodeID string) bool {
	if c.localChannelsAlive(ctx, episodeID) {
		return false
	}
	c.localFailCount.Store(0)
	c.effSignMode.Store(SigningModeUnidbg)
	return true
}

// localChannelsAlive 多通道复核本地是否还有可用通道，三段递进：
// 一、并发探测全部本地通道（ProbePlaybackChannels，结果同时刷新通
// 道健康表）；二、并发全空时在当前播放身份上顺序单发复核 tab 多集
// 与 v5——并发齐射本身会把身份打空（预加载多集齐发即是此形），且
// 身份随存活时长逐通道解封，顺序单发是实测最可靠的确认方式；三、
// 仍全空才丢弃播放身份、换新鲜身份复核 tab 多集。任一处拿到模型
// 即本地可用。探测全程绕过传输层重签，测的是本地签名本身。
func (c *Client) localChannelsAlive(ctx context.Context, episodeID string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	probeCtx = withResignBypass(probeCtx)
	for _, alive := range c.ProbePlaybackChannels(probeCtx) {
		if alive {
			return true
		}
	}
	pb, err := c.playbackIdentity(probeCtx)
	if err != nil {
		return false
	}
	if playback, _ := c.resolvePlaybackViaTabMulti(probeCtx, pb, episodeID); playback != nil {
		c.noteChannelOutcome(probeCtx, channelTabMulti, playback, nil)
		return true
	}
	if playback, _ := c.resolvePlaybackViaMobileV5(probeCtx, pb, episodeID); playback != nil {
		c.noteChannelOutcome(probeCtx, channelV5, playback, nil)
		return true
	}
	c.dropPlaybackIdentityIf(pb)
	pb2, err := c.playbackIdentity(probeCtx)
	if err != nil {
		return false
	}
	if playback, _ := c.resolvePlaybackViaTabMulti(probeCtx, pb2, episodeID); playback != nil {
		c.noteChannelOutcome(probeCtx, channelTabMulti, playback, nil)
		return true
	}
	return false
}

// noteResolveSuccess 处理一次取址成功后的策略动作：本地成功清零失败
// 计数；远程成功则后台探测本地是否已恢复，恢复则切回本地（下一次
// 取址生效）。
func (c *Client) noteResolveSuccess(mode int32, episodeID string) {
	if mode == SigningModeLocal {
		c.localFailCount.Store(0)
		return
	}
	c.probeLocalAsync(episodeID)
}

// probeLocalAsync 远程生效期间后台复探本地是否恢复：与切换判定同一
// 口径——多通道复核（localChannelsAlive），任一本地通道恢复即切回
// 本地（旧实现只探 v5 一条，平台只开 tab 多集的时段里永远切不回
// 来）。只探不取，结果不用于本次播放；单飞运行；探测走内存播放
// 身份，不触碰目录游客身份，失败不影响任何正在使用的身份。
func (c *Client) probeLocalAsync(episodeID string) {
	if !c.localProbeRunning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer c.localProbeRunning.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// 探活测的是本地签名：绕过传输层重签，否则远程签名会把探活
		// 请求也签成远程的，本地没恢复也会误判为已恢复。
		ctx = withResignBypass(ctx)
		if c.localChannelsAlive(ctx, episodeID) {
			c.effSignMode.Store(SigningModeLocal)
			c.localFailCount.Store(0)
		}
	}()
}

// resolvePlaybackViaRemote 按用户所选计算方式走远程签名取址；本地方式或
// 远程未取到模型时返回 (nil, nil)，错误只在远程链路本身失败时返回，
// 供 ResolvePlayback 在本地链也失败时把远程失败原因报给用户。
func (c *Client) resolvePlaybackViaRemote(ctx context.Context, episodeID string) (*model.Playback, error) {
	return c.resolvePlaybackViaRemoteMode(ctx, episodeID, c.signingMode())
}

// resolvePlaybackViaRemoteMode 按给定计算方式走远程签名取址（策略层
// 自动切换时用它按实际生效方式取址）。只有 frida 在这一层分流；
// unidbg 由传输层重签本链请求（resign_transport.go），不经这里。
func (c *Client) resolvePlaybackViaRemoteMode(ctx context.Context, episodeID string, mode int32) (*model.Playback, error) {
	if mode == SigningModeFrida {
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

// fridaTabHosts 是 frida 取址的主机顺序，与服务方客户端一致：B 主机
// 优先，其次 reading.snssdk.com。A 主机（apiOrigin）对 video_tab 单集
// 固定回 ILLEGAL_ACCESS(110)——2026-11-23 在沙箱、盒子与用户手机
// （frida 真签名）三处一致——旧实现只打 A 主机是 frida 一直报错的根源。
func (c *Client) fridaTabHosts() []string {
	first := c.multiOrigin()
	hosts := []string{first}
	if first != "https://reading.snssdk.com" {
		hosts = append(hosts, "https://reading.snssdk.com")
	}
	return hosts
}

// resolvePlaybackViaFrida：身份与签名都来自 frida 服务。取址顺序与
// 服务方客户端一致：video_tab 单集（双主机轮换）优先，全灭再退
// video_tab 多集。查询串的合并规则同样对齐：业务参数在前，服务档案
// 的身份字段覆盖同名键（档案之外的字段不进查询串），最后追加 ts 与
// _rticket；签好的六头与服务档案的 UA 一起发出。单个主机的空响应或
// 错误码只跳过该主机，签名服务本身的错误立即上抛。
func (c *Client) resolvePlaybackViaFrida(ctx context.Context, episodeID string) (*model.Playback, error) {
	prof, err := c.fridaProfileCached(ctx)
	if err != nil {
		return nil, err
	}
	single := [][2]string{
		{"video_id", episodeID},
		{"source", "0"},
		{"need_all_video_definition", "true"},
	}
	var lastErr error
	for _, origin := range c.fridaTabHosts() {
		raw, signFailed, err := c.fridaTabGET(ctx, prof, origin+protocol.PathVideoTabModel, single)
		if signFailed {
			// 签名服务错误与主机无关，直接上抛。
			return nil, err
		}
		if err != nil {
			lastErr = err
			continue
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		playback, perr := parseTabPlayback(raw, episodeID)
		if playback != nil {
			return playback, perr
		}
		lastErr = perr
	}
	multi := [][2]string{
		{"video_id", episodeID},
		{"video_id_type", "0"},
		{"device_level", "1"},
		{"need_all_video_definition", "1"},
		{"need_mp4_align", "0"},
		{"use_os_player", "0"},
		{"video_platform", "1024"},
		{"source", "0"},
		{"from_video_id", ""},
	}
	for _, origin := range c.fridaTabHosts() {
		raw, signFailed, err := c.fridaTabGET(ctx, prof, origin+protocol.PathVideoTabMulti, multi)
		if signFailed {
			return nil, err
		}
		if err != nil {
			lastErr = err
			continue
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		playback, perr := finishMultiPlayback(raw, episodeID)
		if playback != nil {
			return playback, perr
		}
		lastErr = perr
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, ErrAnonymousLimited
}

// fridaTabGET 组装并发出一次 frida 签名的 video_tab GET。signFailed
// 为真表示签名服务本身出错（与主机无关，调用方应立即上抛）；其余
// err 是该主机的传输失败，调用方换下一主机继续。
func (c *Client) fridaTabGET(ctx context.Context, prof *fridaServiceProfile, fullBase string, business [][2]string) (raw []byte, signFailed bool, err error) {
	ticket := c.clock().UnixMilli()
	pairs := make([][2]string, 0, len(business)+len(fridaIdentityKeys)+2)
	pairs = append(pairs, business...)
	for _, k := range fridaIdentityKeys {
		v, ok := prof.params[k]
		if !ok {
			continue
		}
		replaced := false
		for i := range pairs {
			if pairs[i][0] == k {
				pairs[i][1] = v
				replaced = true
				break
			}
		}
		if !replaced {
			pairs = append(pairs, [2]string{k, v})
		}
	}
	pairs = append(pairs,
		[2]string{"ts", strconv.FormatInt(ticket/1000, 10)},
		[2]string{"_rticket", strconv.FormatInt(ticket, 10)})
	fullURL := fullBase + "?" + encodeRemoteQuery(pairs)
	sigs, err := c.fridaSign(ctx, fullURL)
	if err != nil {
		return nil, true, err
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
	raw, _, err = c.doRaw(ctx, "GET", fullURL, headers, nil)
	if err != nil {
		return nil, false, err
	}
	return raw, false, nil
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
