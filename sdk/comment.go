package sdk

import (
	"context"
	"fmt"
	"strings"

	"github.com/beilunyang/openhongguo/internal/protocol"
	"github.com/beilunyang/openhongguo/internal/protocol/sign"
	"github.com/beilunyang/openhongguo/sdk/model"
)

// CommentOptions 选择 comment/list 的一个时间窗口。StartOffsetMs 是窗口
// 在分集时间轴上的起始毫秒（首窗为 0，之后用上一窗的 NextStartMs）；
// Cursor 是上一窗给出的游标（首窗为空）；DurationMs 是分集总时长毫秒。
type CommentOptions struct {
	StartOffsetMs int64
	Cursor        string
	DurationMs    int64
}

// CommentList 加载一集的评论与弹幕，一次一个时间窗口。评论和弹幕是同一
// 条流（commentapi 的 comment_type 20）：按 OffsetMs 渲染即弹幕形态，
// 按 DiggCount 降序即评论形态。episodeID 是分集 id，seriesID 是它所属
// 短剧的 id，两者都必须是真实 id：seriesID 为空或 "0" 时服务端会以
// 参数错误（103001）拒绝，这里先在本地报错。翻页由调用方把返回的
// NextStartMs 和 Cursor 填回 CommentOptions 继续调用；HasMore 为假或
// 某窗没有条目时即到底。匿名身份下发的条数可能少于 ReportedCount。
func (c *Client) CommentList(ctx context.Context, episodeID, seriesID string, opts CommentOptions) (*model.CommentPage, error) {
	return withFreshDevice(ctx, c, false, func(ctx context.Context) (*model.CommentPage, error) {
		if !validID(episodeID) {
			return nil, fmt.Errorf("invalid episode ID")
		}
		if strings.TrimSpace(seriesID) == "" || strings.TrimSpace(seriesID) == "0" || !validID(seriesID) {
			return nil, fmt.Errorf("invalid series ID: comment list needs the real series ID as book_id")
		}
		if opts.StartOffsetMs < 0 {
			return nil, fmt.Errorf("invalid start offset")
		}
		if opts.DurationMs < 0 {
			return nil, fmt.Errorf("invalid duration")
		}
		body, err := protocol.CommentListBody(episodeID, seriesID, opts.StartOffsetMs, opts.DurationMs, opts.Cursor)
		if err != nil {
			return nil, err
		}
		p, err := c.ensureSearchGuest(ctx)
		if err != nil {
			return nil, err
		}
		path := fmt.Sprintf(protocol.PathCommentList, episodeID)
		ticket := c.clock().UnixMilli()
		rawQuery, headers := sign.SignPOST(path, protocol.DeviceQueryPairs(c.identity(p)), ticket, p.DeviceID, protocol.VersionName, body, nil)
		raw, _, err := c.doRaw(ctx, "POST", c.apiOrigin+path+"?"+rawQuery, anonymousHeaders(c, p, ticket, headers), body)
		if err != nil {
			return nil, err
		}
		res, err := protocol.ParseCommentList(raw, opts.StartOffsetMs)
		if err != nil {
			return nil, err
		}
		return &model.CommentPage{
			Items:         res.Items,
			Cursor:        res.Cursor,
			NextStartMs:   res.NextStartMs,
			HasMore:       res.HasMore,
			ReportedCount: res.ReportedCount,
		}, nil
	})
}
