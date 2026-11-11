package sdk

import (
	"context"
	"net/url"

	"github.com/beilunyang/openhongguo/internal/protocol"
	"github.com/beilunyang/openhongguo/sdk/model"
)

// Playback identities are separate from the stored catalog guest: the
// multi playback channels issue for identities registered the way the
// current client generation registers (log5-applog-lf, the enriched
// registration header, a device profile that matches the medusa
// device fingerprint), and such an identity can be single-use — the
// stored guest keeps serving catalog reads regardless. Playback
// identities live in memory only and are never persisted.

// playbackDevice is the device the playback identity registers and
// presents: the same Redmi profile the medusa device proto describes,
// so query, UA and medusa fingerprint agree.
func playbackDevice() model.Device {
	return model.Device{
		Brand:      "Redmi",
		Model:      "25053RT47C",
		OSVersion:  "16",
		OSAPI:      36,
		Build:      "BP2A.250605.031.A3",
		Resolution: "1280*2772",
		DPI:        520,
		Channel:    "update_64",
	}
}

func (c *Client) playbackRegisterOrigin() string {
	if c.registerOrigin != defaultRegisterOrigin {
		// Tests and custom deployments point registration at their own
		// origin; playback registration follows it.
		return c.registerOrigin
	}
	return "https://" + protocol.RegisterHostLF
}

// multiOrigin is the base origin for the multi playback channels: the
// B host in production; a custom API origin (tests, deployments) wins
// when one is configured.
func (c *Client) multiOrigin() string {
	if c.apiOrigin != defaultAPIOrigin {
		return c.apiOrigin
	}
	return "https://" + protocol.APIHostB
}

// playbackIdentity returns the in-memory playback identity, registering
// a fresh one when none is cached.
func (c *Client) playbackIdentity(ctx context.Context) (*DeviceProfile, error) {
	c.playbackMu.Lock()
	defer c.playbackMu.Unlock()
	if guestReady(c.playbackProf) {
		return c.playbackProf, nil
	}
	p, err := c.registerPlaybackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	c.playbackProf = p
	return p, nil
}

// dropPlaybackIdentityIf discards the cached playback identity only
// when it is still the one the caller used, so concurrent failures
// share a single re-registration.
func (c *Client) dropPlaybackIdentityIf(p *DeviceProfile) {
	c.playbackMu.Lock()
	defer c.playbackMu.Unlock()
	if c.playbackProf == p {
		c.playbackProf = nil
	}
}

func (c *Client) registerPlaybackIdentity(ctx context.Context) (*DeviceProfile, error) {
	p, err := NewAnonymousDeviceProfile(nil, c.clock)
	if err != nil {
		return nil, err
	}
	dev := playbackDevice()
	p.Device = &dev
	body, err := protocol.BuildRegisterBody(protocol.RegisterIdentity{
		CDID: p.CDID, OpenUDID: p.OpenUDID, ClientUDID: p.ClientUDID, OAID: p.OAID, ReqID: p.ReqID,
		Device: dev,
	}, c.clock())
	if err != nil {
		return nil, err
	}
	if len(body) < 6 || body[0] != 0x74 || body[1] != 0x63 {
		return nil, ErrSignerUnavailable
	}
	q := url.Values{}
	q.Set("tt_data", "a")
	q.Set("cronet_version", "8d40f833_2026-03-03")
	q.Set("ttnet_version", "4.2.243.31-douyin")
	q.Set("use_store_region_cookie", "1")
	headers := map[string]string{
		"User-Agent":                protocol.UserAgentCronet(dev, protocol.PlaybackVersionCode),
		"Content-Type":              "application/octet-stream;tt-data=a",
		"log-encode-type":           "gzip",
		"x-ss-dp":                   protocol.AID,
		"sdk-version":               "2",
		"x-vc-bdturing-sdk-version": "3.7.0-rc.34-fanqie-xiaoshuo",
		"x-tt-dt":                   p.ReqID,
		"X-SS-STUB":                 protocol.ContentMD5(body),
	}
	raw, cookies, err := c.do(ctx, "POST", c.playbackRegisterOrigin(), protocol.PathRegister, q, headers, body)
	if err != nil {
		return nil, err
	}
	registered, err := protocol.ParseRegister(raw)
	if err != nil {
		return nil, err
	}
	p.DeviceID = registered.DeviceID
	p.InstallID = registered.InstallID
	p.DeviceToken = ""
	applyGuestCookies(p, cookies)
	if !guestReady(p) {
		return nil, ErrAnonymousLimited
	}
	return p, nil
}
