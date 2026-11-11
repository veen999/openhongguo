// Live diagnostic: registers through the real endpoints and resolves
// one episode's playback model, printing what came back. Diagnostic
// branch only; not part of the SDK.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/beilunyang/openhongguo/adapters/httptransport"
	"github.com/beilunyang/openhongguo/adapters/memstore"
	"github.com/beilunyang/openhongguo/sdk"
)

func main() {
	vid := "7699003626647194649"
	if len(os.Args) > 1 {
		vid = os.Args[1]
	}
	client, err := sdk.NewClient(sdk.Options{Transport: httptransport.New(), ProfileStore: memstore.New()})
	if err != nil {
		fmt.Println("CLIENT ERROR:", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pb, err := client.ResolvePlayback(ctx, vid, sdk.PlaybackOptions{})
	if pb == nil {
		fmt.Printf("RESOLVE FAILED vid=%s err=%v\n", vid, err)
		os.Exit(2)
	}
	fmt.Printf("RESOLVE OK vid=%s protected=%v err=%v streams=%d\n", vid, pb.Protected, err, len(pb.Streams))
	for _, s := range pb.Streams {
		fmt.Printf("  stream quality=%s %dx%d codec=%s bitrate=%d url_len=%d\n", s.Definition, s.Width, s.Height, s.Codec, s.Bitrate, len(s.URL))
	}
	if pb.Protected {
		key, kerr := sdk.ContentKey(pb)
		fmt.Printf("CONTENTKEY keylen=%d err=%v\n", len(key.Hex), kerr)
	}
}
