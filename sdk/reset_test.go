package sdk_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/beilunyang/openhongguo/adapters/httptransport"
	"github.com/beilunyang/openhongguo/adapters/memstore"
	"github.com/beilunyang/openhongguo/internal/protocol"
	"github.com/beilunyang/openhongguo/sdk"
)

// devicePerRequest 模拟 2026-10-29 起的服务端：累计注册数不超过 emptyFor 时，
// 所有取流通道（multi/tab/单集）都返回空结果；累计注册数不超过 rejectFor 时
// 搜索报设备被拒。注册计数包含播放专用身份的注册。
type devicePerRequest struct {
	mu        sync.Mutex
	registers int
	playbacks int
	emptyFor  int
	rejectFor int
	searchErr bool
}

func (s *devicePerRequest) handler(t *testing.T) http.Handler {
	reg := fixture(t, "register-response.json")
	clear := fixture(t, "clear-playback.json")
	search := fixture(t, "search-response.json")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case protocol.PathRegister:
			s.registers++
			w.Header().Add("Set-Cookie", "ttreq=fixture-ticket; Path=/")
			_, _ = w.Write(reg)
		case protocol.PathPlayback, protocol.PathPlaybackMulti, protocol.PathVideoTabMulti, protocol.PathVideoTabModel:
			s.playbacks++
			if s.registers <= s.emptyFor {
				return
			}
			_, _ = w.Write(clear)
		case protocol.PathSearch:
			if s.searchErr {
				return
			}
			if s.registers <= s.rejectFor {
				_, _ = w.Write([]byte(`{"BaseResp":{"StatusCode":263175}}`))
				return
			}
			_, _ = w.Write(search)
		default:
			http.NotFound(w, r)
		}
	})
}

// syncLog 是可并发写入的 Logger。
type syncLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *syncLog) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func resetClient(t *testing.T, h http.Handler, resets *int) (*sdk.Client, *syncLog) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	logs := &syncLog{}
	client, err := sdk.NewClient(sdk.Options{
		Transport: httptransport.New(), ProfileStore: memstore.New(),
		APIOrigin: srv.URL, RegisterOrigin: srv.URL, DistributionOrigin: srv.URL,
		Logger: logs, DeviceResets: resets,
		Clock: func() time.Time { return time.UnixMilli(1_700_000_000_123).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, logs
}

func TestEmptyPlaybackMovesToFreshDevice(t *testing.T) {
	// 第一台播放身份两条 multi 通道都空 → 丢弃并重新注册一台，
	// 第二台的 tab-multi 拿到模型。目录游客全程不参与。
	s := &devicePerRequest{emptyFor: 1}
	client, _ := resetClient(t, s.handler(t), nil)
	pb, err := client.ResolvePlayback(context.Background(), "7100000000000201", sdk.PlaybackOptions{})
	if err != nil || pb == nil {
		t.Fatalf("pb = %v err = %v", pb, err)
	}
	if s.registers != 2 || s.playbacks != 3 {
		t.Fatalf("registers = %d playbacks = %d", s.registers, s.playbacks)
	}
}

func TestEmptyPlaybackGivesUpAfterDefaultResets(t *testing.T) {
	// 全部通道恒空：每轮 = 播放身份 2 次注册（首注 + 丢弃重注）+
	// 目录游客 1 次注册，播放请求 5 次（multi×2 + tab-multi 重试 +
	// tab 单集 + player 单集）；withFreshDevice 默认 2 次重置共 3 轮。
	s := &devicePerRequest{emptyFor: 99}
	client, _ := resetClient(t, s.handler(t), nil)
	_, err := client.ResolvePlayback(context.Background(), "7100000000000201", sdk.PlaybackOptions{})
	if !errors.Is(err, sdk.ErrAnonymousLimited) {
		t.Fatalf("err = %v", err)
	}
	if s.registers != 7 || s.playbacks != 15 {
		t.Fatalf("registers = %d playbacks = %d", s.registers, s.playbacks)
	}
}

func TestZeroDeviceResetsDisablesRecovery(t *testing.T) {
	// DeviceResets=0 只关目录游客的重置：播放身份自带的一次重注仍在，
	// 一轮之内注册 3 次（播放×2 + 游客×1）后以受限告终。
	s := &devicePerRequest{emptyFor: 99}
	client, _ := resetClient(t, s.handler(t), sdk.Int(0))
	_, err := client.ResolvePlayback(context.Background(), "7100000000000201", sdk.PlaybackOptions{})
	if !errors.Is(err, sdk.ErrAnonymousLimited) || s.registers != 3 {
		t.Fatalf("err = %v registers = %d", err, s.registers)
	}
}

func TestEmptyReadOutsidePlaybackKeepsDevice(t *testing.T) {
	s := &devicePerRequest{searchErr: true}
	client, _ := resetClient(t, s.handler(t), nil)
	_, err := client.Search(context.Background(), "短剧", sdk.SearchOptions{})
	if !errors.Is(err, sdk.ErrAnonymousLimited) || s.registers != 1 {
		t.Fatalf("err = %v registers = %d", err, s.registers)
	}
}

func TestRejectedDeviceIsReplacedOnAnyRead(t *testing.T) {
	s := &devicePerRequest{rejectFor: 1}
	client, _ := resetClient(t, s.handler(t), nil)
	if _, err := client.Search(context.Background(), "短剧", sdk.SearchOptions{}); err != nil {
		t.Fatal(err)
	}
	if s.registers != 2 {
		t.Fatalf("registers = %d", s.registers)
	}
}

func TestConcurrentFailuresRegisterOneDevice(t *testing.T) {
	// emptyFor=2：搜索先注册目录游客（1），播放身份首注（2）仍空；
	// 8 个并发失败应共享同一次播放身份重注（3），而不是各注一台。
	s := &devicePerRequest{emptyFor: 2}
	client, _ := resetClient(t, s.handler(t), nil)
	// 先注册第一台设备，让并发调用都从同一台设备出发。
	if _, err := client.Search(context.Background(), "短剧", sdk.SearchOptions{}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := client.ResolvePlayback(context.Background(), "7100000000000201", sdk.PlaybackOptions{})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if s.registers != 3 {
		t.Fatalf("registers = %d, want 3", s.registers)
	}
}
