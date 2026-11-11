package protocol

import (
	"fmt"

	"github.com/beilunyang/openhongguo/sdk/model"
)

// UserAgent is the app's Cronet User-Agent for d, in the captured client's
// format (2026-10-24), which names the model code rather than a market name.
func UserAgent(d model.Device) string {
	return UserAgentFor(d, VersionCode)
}

// UserAgentFor is UserAgent under an explicit version profile.
func UserAgentFor(d model.Device, versionCode string) string {
	d = d.OrDefault()
	return fmt.Sprintf("%s/%s (Linux; U; Android %s; zh_CN; %s; Build/%s; Cronet/TTNetVersion:8d40f833 2026-03-03 QuicVersion:21ac1950 2025-11-18)",
		Package, versionCode, d.OSVersion, d.Model, d.Build)
}

// UserAgentCronet is the User-Agent the current client generation sends
// on playback and playback-registration requests, with its Cronet build
// string (04657795 2026-01-23).
func UserAgentCronet(d model.Device, versionCode string) string {
	d = d.OrDefault()
	return fmt.Sprintf("%s/%s (Linux; U; Android %s; zh_CN; %s; Build/%s; Cronet/TTNetVersion:04657795 2026-01-23 QuicVersion:c67e9834 2025-09-08)",
		Package, versionCode, d.OSVersion, d.Model, d.Build)
}
