package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type testClocks struct {
	wall time.Time
	mono time.Time
}

func newTestReadings() (*readings, *testClocks) {
	now := time.Date(2026, 11, 19, 15, 0, 0, 0, time.UTC)
	clock := &testClocks{wall: now, mono: now}
	return &readings{
		wallNow: func() time.Time { return clock.wall },
		monoNow: func() time.Time { return clock.mono },
	}, clock
}

func TestJSONResponses(t *testing.T) {
	const (
		lightPath = "/sensor/ambient_light"
		colorPath = "/sensor/ambient_color_temperature"
		wantLight = `{"id":"sensor-ambient_light","state":"44.0 lx","value":44.0}`
		wantError = `{"error":"no ambient light reading available"}`
		wantColor = `{"error":"no color sensor"}`
		want404   = `{"detail":"Not Found"}`
		want405   = `{"detail":"Method Not Allowed"}`
	)
	for _, tt := range []struct {
		name, method, path string
		hasReading         bool
		status             int
		body, allow        string
	}{
		{"reading", http.MethodGet, lightPath, true, 200, wantLight, ""},
		{"unavailable", http.MethodGet, lightPath, false, 503, wantError, ""},
		{"no color sensor", http.MethodGet, colorPath, false, 404, wantColor, ""},
		{"unknown path", http.MethodGet, "/unknown", false, 404, want404, ""},
		{"unknown path with post", http.MethodPost, "/unknown", false, 404, want404, ""},
		{"root", http.MethodGet, "/", false, 404, want404, ""},
		{"trailing slash", http.MethodGet, lightPath + "/", false, 404, want404, ""},
		{"unclean path", http.MethodGet, "/sensor//ambient_light", false, 404, want404, ""},
		{"escaped slash", http.MethodGet, "/sensor%2Fambient_light", true, 200, wantLight, ""},
		{"escaped letter", http.MethodGet, "/sensor/%61mbient_light", true, 200, wantLight, ""},
		{"light post", http.MethodPost, lightPath, true, 405, want405, "GET"},
		{"light head", http.MethodHead, lightPath, true, 405, want405, "GET"},
		{"color options", http.MethodOptions, colorPath, false, 405, want405, "GET"},
		{"color head", http.MethodHead, colorPath, false, 405, want405, "GET"},
		{"events put", http.MethodPut, "/events", false, 405, want405, "GET"},
		{"events head", http.MethodHead, "/events", false, 405, want405, "GET"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rs, _ := newTestReadings()
			if tt.hasReading {
				rs.store(44.0)
			}
			server := bridgeServer{readings: rs}
			response := httptest.NewRecorder()
			server.handler().ServeHTTP(response, httptest.NewRequest(tt.method, tt.path, nil))

			if got := response.Code; got != tt.status {
				t.Errorf("status = %d, want %d", got, tt.status)
			}
			if got := response.Body.String(); got != tt.body {
				t.Errorf("body = %q, want %q", got, tt.body)
			}
			if got := response.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			if got := response.Header().Get("Allow"); got != tt.allow {
				t.Errorf("Allow = %q, want %q", got, tt.allow)
			}
		})
	}
}

type streamRecorder struct {
	*httptest.ResponseRecorder
	ctx    context.Context
	frames chan string
}

func (w *streamRecorder) Flush() {
	w.ResponseRecorder.Flush()
	frame := w.Body.String()
	w.Body.Reset()
	select {
	case w.frames <- frame:
	case <-w.ctx.Done():
	}
}

func (w *streamRecorder) nextFrame(t *testing.T) string {
	t.Helper()
	select {
	case frame := <-w.frames:
		return frame
	case <-time.After(time.Second):
		t.Error("timed out waiting for stream flush")
		return ""
	}
}

func TestEvents(t *testing.T) {
	const state = "event: state\r\ndata: {\"id\": \"sensor-ambient_light\", \"state\": \"44.0 lx\", \"value\": 44.0}\r\n\r\n"
	for _, tt := range []struct {
		name       string
		hasReading bool
		pingPeriod time.Duration
	}{
		{"reading", true, time.Hour},
		{"no reading", false, 25 * time.Millisecond},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rs, clock := newTestReadings()
			clock.wall = time.Date(2026, 11, 19, 16, 10, 6, 272839123, time.FixedZone("east", 3600))
			if tt.hasReading {
				rs.store(44.0)
			}
			server := bridgeServer{
				readings:      rs,
				eventInterval: 3 * time.Millisecond,
				pingInterval:  tt.pingPeriod,
			}
			ctx, cancel := context.WithCancel(t.Context())
			response := &streamRecorder{ResponseRecorder: httptest.NewRecorder(), ctx: ctx, frames: make(chan string)}
			request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/events", nil)
			done := make(chan struct{})
			go func() {
				defer close(done)
				server.handler().ServeHTTP(response, request)
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("stream handler did not return after disconnect")
					return
				}
				if response.Code != http.StatusOK {
					t.Errorf("status = %d, want 200", response.Code)
				}
				headers := response.Result().Header
				for key, want := range map[string]string{
					"Cache-Control":     "no-store",
					"Connection":        "keep-alive",
					"X-Accel-Buffering": "no",
					"Content-Type":      "text/event-stream; charset=utf-8",
				} {
					if got := headers.Get(key); got != want {
						t.Errorf("%s = %q, want %q", key, got, want)
					}
				}
			})

			if got := response.nextFrame(t); got != "" {
				t.Fatalf("first flush = %q, want headers only", got)
			}
			for i := 0; i < 2; i++ {
				got := response.nextFrame(t)
				if tt.hasReading {
					if got != state {
						t.Errorf("state = %q, want %q", got, state)
					}
					continue
				}
				if want := ": ping - 2026-11-19 15:10:06.272839+00:00\r\n\r\n"; got != want {
					t.Errorf("ping = %q, want %q", got, want)
				}
			}
		})
	}
}

func TestSampleFreshness(t *testing.T) {
	t.Run("strips monotonic time", func(t *testing.T) {
		rs, _ := newTestReadings()
		rs.wallNow = time.Now
		rs.store(44.0)
		if got := rs.sampleAt; got != got.Round(0) {
			t.Error("stored timestamp includes monotonic time")
		}
	})

	for _, tt := range []struct {
		name    string
		wallAge time.Duration
		monoAge time.Duration
		want    bool
	}{
		{"at limit", 10 * time.Second, 10 * time.Second, true},
		{"just past limit", 10*time.Second + time.Nanosecond, 10*time.Second + time.Nanosecond, false},
		{"after sleep", time.Hour, time.Second, false},
		{"clock set back", -time.Second, time.Second, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rs, clock := newTestReadings()
			rs.store(44.0)
			clock.wall = clock.wall.Add(tt.wallAge)
			clock.mono = clock.mono.Add(tt.monoAge)
			checkLux(t, rs, 44.0, tt.want)
		})
	}
}

func TestLastReading(t *testing.T) {
	rs, clock := newTestReadings()
	checkLux(t, rs, 0, false)

	rs.store(44.0)
	checkLux(t, rs, 44.0, true)
	clock.wall = clock.wall.Add(8 * time.Second)
	clock.mono = clock.mono.Add(8 * time.Second)
	checkLux(t, rs, 44.0, true)

	clock.wall = clock.wall.Add(time.Hour)
	clock.mono = clock.mono.Add(30 * time.Second)
	checkLux(t, rs, 44.0, true)
	clock.mono = clock.mono.Add(time.Nanosecond)
	checkLux(t, rs, 0, false)
	checkLux(t, rs, 0, false)

	rs.store(0.0)
	checkLux(t, rs, 0.0, true)
}

func checkLux(t *testing.T, rs *readings, want float64, wantOK bool) {
	t.Helper()
	if got, ok := rs.lux(); ok != wantOK || ok && got != want {
		t.Errorf("lux() = (%v, %v), want (%v, %v)", got, ok, want, wantOK)
	}
}

func TestSampler(t *testing.T) {
	rs, clock := newTestReadings()
	smp := sampler{
		readings:    rs,
		read:        func() (sensorReading, error) { return sensorReading{lux: 44.5}, errors.New("sensor failed") },
		exit:        func(code int) { t.Errorf("unexpected exit(%d)", code) },
		hangTimeout: time.Second,
	}
	smp.sample()
	checkLux(t, rs, 0, false)

	smp.read = func() (sensorReading, error) { return sensorReading{lux: 42.06}, nil }
	smp.sample()
	checkLux(t, rs, 42.1, true)

	clock.wall = clock.wall.Add(time.Second)
	smp.read = func() (sensorReading, error) { return sensorReading{}, errors.New("sensor failed") }
	smp.sample()
	checkLux(t, rs, 42.1, true)
	if want := clock.wall.Add(-time.Second).Round(0); rs.sampleAt != want {
		t.Errorf("failed read changed sample timestamp to %v, want %v", rs.sampleAt, want)
	}
}

func captureLog(t *testing.T) *bytes.Buffer {
	output, flags := log.Writer(), log.Flags()
	var logs bytes.Buffer
	log.SetOutput(&logs)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(output)
		log.SetFlags(flags)
	})
	return &logs
}

func TestSamplerSourceChange(t *testing.T) {
	logs := captureLog(t)
	rs, _ := newTestReadings()
	a := source{product: "Studio Display", vendorID: 0x05ac, productID: 0x1114, locationID: 0x20140000}
	b := source{product: "Other Display", vendorID: 0x05ac, productID: 0x1114, locationID: 0x20200000}
	var reading sensorReading
	var readErr error
	smp := sampler{
		readings:    rs,
		read:        func() (sensorReading, error) { return reading, readErr },
		exit:        func(code int) { t.Errorf("unexpected exit(%d)", code) },
		hangTimeout: time.Second,
	}
	for _, src := range []source{a, a, a, b, b} {
		reading = sensorReading{lux: 18.94, source: src, matches: 1}
		smp.sample()
	}
	readErr = errors.New("sensor failed")
	smp.sample()
	readErr = nil
	smp.sample()

	const otherReading = "reading: 18.9 lx from Other Display (05ac:1114, location 0x20200000)\n"
	const want = "reading: 18.9 lx from Studio Display (05ac:1114, location 0x20140000)\n" +
		otherReading + "no reading: sensor failed\n" + otherReading
	if got := logs.String(); got != want {
		t.Errorf("log = %q, want %q", got, want)
	}
}

func TestSamplerMultiMatch(t *testing.T) {
	logs := captureLog(t)
	rs, _ := newTestReadings()
	reading := sensorReading{
		lux:    18.9,
		source: source{product: "Studio Display", vendorID: 0x05ac, productID: 0x1114, locationID: 0x20140000},
	}
	smp := sampler{
		readings:    rs,
		read:        func() (sensorReading, error) { return reading, nil },
		exit:        func(code int) { t.Errorf("unexpected exit(%d)", code) },
		hangTimeout: time.Second,
	}
	const readingLog = "reading: 18.9 lx from Studio Display (05ac:1114, location 0x20140000)\n"
	const warning2 = "warning: 2 ambient light sensors match, using Studio Display (05ac:1114, location 0x20140000)\n"
	const warning3 = "warning: 3 ambient light sensors match, using Studio Display (05ac:1114, location 0x20140000)\n"
	for _, tt := range []struct {
		matches int
		wantLog string
	}{
		{1, readingLog},
		{2, warning2},
		{2, ""},
		{3, warning3},
		{1, ""},
		{2, warning2},
	} {
		reading.matches = tt.matches
		smp.sample()
		if got := logs.String(); got != tt.wantLog {
			t.Errorf("log with %d matches = %q, want %q", tt.matches, got, tt.wantLog)
		}
		logs.Reset()
	}
}

func TestSourceString(t *testing.T) {
	for _, tt := range []struct {
		name string
		src  source
		want string
	}{
		{"external", source{"Studio Display", 0x05ac, 0x1114, 0x20140000, false}, "Studio Display (05ac:1114, location 0x20140000)"},
		{"unknown", source{}, "unknown sensor (0000:0000, location 0x00000000)"},
		{"built-in", source{"Internal", 0x05ac, 0x0001, 0x20, true}, "Internal (05ac:0001, location 0x00000020, built-in)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.src.String(); got != tt.want {
				t.Errorf("source.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSamplerWatchdog(t *testing.T) {
	rs, _ := newTestReadings()
	release := make(chan struct{})
	exited := make(chan int, 1)
	smp := sampler{
		readings: rs,
		read: func() (sensorReading, error) {
			<-release
			return sensorReading{lux: 42.0}, nil
		},
		exit:        func(code int) { exited <- code },
		hangTimeout: 10 * time.Millisecond,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		smp.sample()
	}()
	t.Cleanup(func() {
		close(release)
		select {
		case <-done:
			checkLux(t, rs, 0, false)
		case <-time.After(time.Second):
			t.Error("sampler did not return after releasing the reader")
		}
	})
	select {
	case got := <-exited:
		if got != 1 {
			t.Errorf("exit(%d), want exit(1)", got)
		}
	case <-time.After(time.Second):
		t.Fatal("watchdog did not call exit")
	}
}
