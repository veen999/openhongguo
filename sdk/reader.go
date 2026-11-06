package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/beilunyang/openhongguo/internal/protocol"
	"github.com/beilunyang/openhongguo/internal/protocol/sign"
	"github.com/beilunyang/openhongguo/sdk/model"
)

// AudioPlayback 加载一章有声书的播放模型，形状与 ResolvePlayback 相同：
// 各码流的地址、编码、大小。音频同样是 cenc 加密时与 ErrDRMProtected
// 一起返回，交给 ContentKey 取得密钥，之后走与视频相同的解密路径。
//
// 取模接口是 GET /reading/reader/audio/playinfo/，按章单独调用：
// 一次传多章时服务端返回的 item_id 会错位（2026-11-06 实测），所以
// 这里只接受单个章节。
func (c *Client) AudioPlayback(ctx context.Context, bookID, itemID string, toneID int) (*model.Playback, error) {
	return withFreshDevice(ctx, c, true, func(ctx context.Context) (*model.Playback, error) {
		if !validID(bookID) || !validID(itemID) {
			return nil, fmt.Errorf("invalid book or item ID")
		}
		p, err := c.ensureSearchGuest(ctx)
		if err != nil {
			return nil, err
		}
		ticket := c.clock().UnixMilli()
		rawQuery, headers := sign.SignGET(protocol.SignedAudioPlayInfoPairs(c.identity(p), bookID, itemID, toneID), ticket, p.DeviceID, protocol.VersionName, nil)
		raw, _, err := c.doRaw(ctx, "GET", c.apiOrigin+protocol.PathAudioPlayInfo+"?"+rawQuery, anonymousHeaders(c, p, ticket, headers), nil)
		if err != nil {
			return nil, err
		}
		playback, err := protocol.ParseAudioPlayInfo(raw, itemID)
		if err != nil {
			if errors.Is(err, protocol.ErrEmptyModel) {
				return nil, ErrAnonymousLimited
			}
			return nil, err
		}
		if playback.Protected {
			return playback, ErrDRMProtected
		}
		return playback, nil
	})
}

// ToneInfo 查询一本书的音色信息：是否有可播音色，以及本书若是有声书时
// 对应的小说 book_id。小说详情页用它决定「开始收听」指向哪本书。
func (c *Client) ToneInfo(ctx context.Context, bookID string) (*model.ToneInfo, error) {
	return withFreshDevice(ctx, c, false, func(ctx context.Context) (*model.ToneInfo, error) {
		if !validID(bookID) {
			return nil, fmt.Errorf("invalid book ID")
		}
		p, err := c.ensureSearchGuest(ctx)
		if err != nil {
			return nil, err
		}
		ticket := c.clock().UnixMilli()
		rawQuery, headers := sign.SignGET(protocol.SignedToneInfoPairs(c.identity(p), bookID), ticket, p.DeviceID, protocol.VersionName, nil)
		raw, _, err := c.doRaw(ctx, "GET", c.apiOrigin+protocol.PathAudioToneInfo+"?"+rawQuery, anonymousHeaders(c, p, ticket, headers), nil)
		if err != nil {
			return nil, err
		}
		return protocol.ParseToneInfo(raw)
	})
}

// ChapterContent 读取一章小说正文并解密。正文的内容键与设备身份绑定，
// 每个身份只需向 /reading/crypt/registerkey 注册一次（见 ensureContentKey）。
//
// 章节对匿名身份未解锁时（未注册成功、或付费/VIP 章节）服务端返回
// content="Invalid"，本方法将其归为 ErrContentLocked，调用方应提示付费，
// 不要当作故障重试。
func (c *Client) ChapterContent(ctx context.Context, bookID, itemID string) (*model.ChapterContent, error) {
	return withFreshDevice(ctx, c, false, func(ctx context.Context) (*model.ChapterContent, error) {
		if !validID(bookID) || !validID(itemID) {
			return nil, fmt.Errorf("invalid book or item ID")
		}
		content, err := c.chapterContentOnce(ctx, bookID, itemID)
		if err == nil {
			return content, nil
		}
		if !errors.Is(err, errStaleContentKey) {
			return nil, err
		}
		// 缓存的键解不开（服务端换过键）：丢弃缓存、重新注册、再试一次。
		if _, rerr := c.registerContentKey(ctx, true); rerr != nil {
			return nil, rerr
		}
		return c.chapterContentOnce(ctx, bookID, itemID)
	})
}

// errStaleContentKey 表示缓存的正文内容键解不开当前章节。
var errStaleContentKey = errors.New("openhongguo: cached content key does not decrypt")

func (c *Client) chapterContentOnce(ctx context.Context, bookID, itemID string) (*model.ChapterContent, error) {
	keyHex, err := c.registerContentKey(ctx, false)
	if err != nil {
		return nil, err
	}
	p, err := c.ensureSearchGuest(ctx)
	if err != nil {
		return nil, err
	}
	ticket := c.clock().UnixMilli()
	rawQuery, headers := sign.SignGET(protocol.SignedReaderFullPairs(c.identity(p), bookID, itemID), ticket, p.DeviceID, protocol.VersionName, nil)
	raw, _, err := c.doRaw(ctx, "GET", c.apiOrigin+protocol.PathReaderFull+"?"+rawQuery, anonymousHeaders(c, p, ticket, headers), nil)
	if err != nil {
		return nil, err
	}
	full, err := protocol.ParseReaderFull(raw)
	if err != nil {
		return nil, err
	}
	if full.Content == "" || full.Content == "Invalid" {
		return nil, ErrContentLocked
	}
	chapterHTML, err := protocol.DecryptChapterContent(full.Content, keyHex)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errStaleContentKey, err)
	}
	return &model.ChapterContent{
		Title:      full.Title,
		HTML:       chapterHTML,
		Paragraphs: chapterParagraphs(chapterHTML),
	}, nil
}

// registerContentKey 返回当前设备身份的正文内容键（大写 hex）。
// 已注册过就用缓存；force 为真时丢弃缓存重新注册。
// 注册把（设备 id、用户 id=0）组成的种子用客户端内置固定键加密后提交，
// 服务端把内容键绑定到该身份并回传同样被固定键包裹的键材料。
func (c *Client) registerContentKey(ctx context.Context, force bool) (string, error) {
	p, err := c.ensureSearchGuest(ctx)
	if err != nil {
		return "", err
	}
	c.contentKeyMu.Lock()
	if c.contentKeys == nil {
		c.contentKeys = map[string]string{}
	}
	if !force {
		if k, ok := c.contentKeys[p.DeviceID]; ok {
			c.contentKeyMu.Unlock()
			return k, nil
		}
	}
	c.contentKeyMu.Unlock()

	deviceID, err := strconv.ParseInt(p.DeviceID, 10, 64)
	if err != nil {
		return "", fmt.Errorf("guest device id %q is not numeric: %w", p.DeviceID, err)
	}
	content, err := protocol.BuildRegisterKeyContent(deviceID)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]any{"content": content, "keyver": 1})
	if err != nil {
		return "", err
	}
	ticket := c.clock().UnixMilli()
	rawQuery, headers := sign.SignPOST(protocol.PathRegisterKey, protocol.DeviceQueryPairs(c.identity(p)), ticket, p.DeviceID, protocol.VersionName, body, nil)
	raw, _, err := c.doRaw(ctx, "POST", c.apiOrigin+protocol.PathRegisterKey+"?"+rawQuery, anonymousHeaders(c, p, ticket, headers), body)
	if err != nil {
		return "", err
	}
	keyB64, err := protocol.ParseRegisterKey(raw)
	if err != nil {
		return "", err
	}
	keyHex, err := protocol.DeriveContentKey(keyB64)
	if err != nil {
		return "", err
	}
	c.contentKeyMu.Lock()
	c.contentKeys[p.DeviceID] = keyHex
	c.contentKeyMu.Unlock()
	return keyHex, nil
}

var (
	blkPattern = regexp.MustCompile(`(?s)<blk[^>]*>(.*?)</blk>`)
	tagPattern = regexp.MustCompile(`(?s)<[^>]+>`)
)

// chapterParagraphs 从章节 HTML 中按顺序抽出纯文本段落。
// 正文段落是 <blk> 元素；没有 <blk> 时退化为去标签后按行切分。
func chapterParagraphs(chapterHTML string) []string {
	var out []string
	push := func(s string) {
		s = strings.TrimSpace(html.UnescapeString(tagPattern.ReplaceAllString(s, "")))
		if s != "" {
			out = append(out, s)
		}
	}
	if matches := blkPattern.FindAllStringSubmatch(chapterHTML, -1); len(matches) > 0 {
		for _, m := range matches {
			push(m[1])
		}
		return out
	}
	for _, line := range strings.Split(tagPattern.ReplaceAllString(chapterHTML, "\n"), "\n") {
		push(line)
	}
	return out
}
