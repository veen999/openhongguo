package sdk

import (
 "context"
 "fmt"
)

// DebugChannels runs each playback issuance channel exactly once for
// diagnostics and reports per-channel outcome. Temporary probe aid.
func (c *Client) DebugChannels(ctx context.Context, episodeID string) map[string]string {
 out := map[string]string{}
 rec := func(name string, streams int, err error) {
   out[name] = fmt.Sprintf("streams=%d err=%v", streams, err)
 }
 if pb, err := c.playbackIdentity(ctx); err != nil {
   out["identity"] = fmt.Sprintf("err=%v", err)
 } else {
   out["identity"] = fmt.Sprintf("device_id=%s iid=%s", pb.DeviceID, pb.InstallID)
   if p, e := c.resolvePlaybackViaMobileV5(ctx, pb, episodeID); p != nil { rec("v5", len(p.Streams), e) } else { rec("v5", -1, e) }
   if p, e := c.resolvePlaybackViaMultiPlayerV2(ctx, pb, episodeID); p != nil { rec("multiV2", len(p.Streams), e) } else { rec("multiV2", -1, e) }
   if p, e := c.resolvePlaybackViaMultiPlayer(ctx, pb, episodeID); p != nil { rec("multi", len(p.Streams), e) } else { rec("multi", -1, e) }
   if p, e := c.resolvePlaybackViaTabMulti(ctx, pb, episodeID); p != nil { rec("tabMulti", len(p.Streams), e) } else { rec("tabMulti", -1, e) }
 }
 if p, e := c.resolvePlaybackViaPlayer(ctx, episodeID); p != nil { rec("player", len(p.Streams), e) } else { rec("player", -1, e) }
 if p, e := c.resolvePlaybackViaTab(ctx, episodeID); p != nil { rec("tab", len(p.Streams), e) } else { rec("tab", -1, e) }
 return out
}
