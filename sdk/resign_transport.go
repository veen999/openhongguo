package sdk

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// resignTransport 是远程计算-unidbg 的接入点，形态与上游 openhongguo
// v0.4.0 的 remoteSigner 一致（其 Options.SignerURL 机制）：本地链照常
// 构造请求并本地签名，请求发出前若实际生效方式为 unidbg 且这是播放
// 取址请求（带 x-medusa），就把完整 URL 与请求体交给签名服务
// （POST /feature-hash），用服务返回的六个签名头替换本地算出的签名头
// 再发出。签名服务失败时不发出请求，错误带「远程计算-unidbg」字样
// 向上返回，不静默回退本地签名。
//
// 只作用于播放取址（/player/ 与 video_tab 路径）：目录、搜索、评论等
// 读取不依赖该服务，服务不可用时不受影响。此前版本把远程签名接在
// video_tab 单集 GET 上，该通道全天被平台拒绝（code 110 或空响应），
// 而 v5 POST 才是平台实际发放、且上游修复包重签的请求——本文件即
// 为纠正这一接入点而设。
type resignTransport struct {
	next   Transport
	client *Client
}

type resignBypassKey struct{}

// withResignBypass 标记一段调用链不做远程重签。策略层的本地探活必须
// 测本地签名本身是否恢复，不能被重签污染，否则永远切不回去。
func withResignBypass(ctx context.Context) context.Context {
	return context.WithValue(ctx, resignBypassKey{}, true)
}

func (t *resignTransport) Do(ctx context.Context, req *Request) (*Response, error) {
	if t.client == nil || t.client.effectiveSigningMode() != SigningModeUnidbg ||
		ctx.Value(resignBypassKey{}) != nil || !isPlaybackRequest(req) {
		return t.next.Do(ctx, req)
	}
	if err := t.sign(ctx, req); err != nil {
		return nil, err
	}
	return t.next.Do(ctx, req)
}

// isPlaybackRequest 判断请求是否属于播放取址且带本地签名头。
func isPlaybackRequest(req *Request) bool {
	if req == nil {
		return false
	}
	hasMedusa := false
	for k := range req.Headers {
		if strings.EqualFold(k, "x-medusa") {
			hasMedusa = true
			break
		}
	}
	if !hasMedusa {
		return false
	}
	return strings.Contains(req.URL, "/player/") || strings.Contains(req.URL, "video_tab")
}

// sign 把请求的 URL（与请求体，如有）交给 unidbg 签名服务，并把请求
// 头里的六个签名头整体替换为服务返回的签名头（口径与上游一致：
// 服务未返回 x-medusa 视为无签名、报错）。
func (t *resignTransport) sign(ctx context.Context, req *Request) error {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	in := map[string]string{"url": req.URL}
	if len(req.Body) > 0 {
		in["body_hex"] = hex.EncodeToString(req.Body)
	}
	var payload bytes.Buffer
	enc := json.NewEncoder(&payload)
	enc.SetEscapeHTML(false) // 保留 URL 里的 &，与上游一致
	if err := enc.Encode(in); err != nil {
		return err
	}
	resp, err := t.next.Do(ctx, &Request{
		Method: "POST", URL: unidbgSignerEndpoint + "/feature-hash",
		Headers: map[string]string{"Content-Type": "application/json", "Accept-Encoding": "identity"},
		Body:    payload.Bytes(),
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("远程计算-unidbg服务未连接")
	}
	body, err := decodeBody(resp)
	if err != nil {
		return fmt.Errorf("远程计算-unidbg服务返回异常")
	}
	if resp.StatusCode != 200 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return fmt.Errorf("远程计算-unidbg服务返回错误：%s", e.Error)
		}
		return fmt.Errorf("远程计算-unidbg服务不可用（HTTP %d）", resp.StatusCode)
	}
	var out struct {
		Headers map[string]any `json:"headers"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Headers == nil {
		return fmt.Errorf("远程计算-unidbg未返回签名头")
	}
	sigs, err := extractSignatureHeaders(out.Headers, "远程计算-unidbg")
	if err != nil {
		return err
	}
	for name := range req.Headers {
		for _, h := range signatureHeaderNames {
			if strings.EqualFold(name, h) {
				delete(req.Headers, name)
			}
		}
	}
	for k, v := range sigs {
		req.Headers[k] = v
	}
	return nil
}
