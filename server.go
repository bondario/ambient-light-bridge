package main

import (
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	maxSampleAge = 10 * time.Second
	maxHoldAge   = 30 * time.Second
	pingLayout   = "2006-01-02 15:04:05.000000+00:00"
)

type readings struct {
	wallNow func() time.Time
	monoNow func() time.Time

	mu        sync.Mutex
	sampleLux float64
	sampleAt  time.Time
	lastLux   float64
	lastLuxAt time.Time
}

func (r *readings) store(lux float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sampleLux = lux
	// Wall time keeps aging the sample while the Mac sleeps.
	r.sampleAt = r.wallNow().Round(0)
}

func (r *readings) lux() (float64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.sampleAt.IsZero() {
		age := r.wallNow().Sub(r.sampleAt)
		if 0 <= age && age <= maxSampleAge {
			r.lastLux, r.lastLuxAt = r.sampleLux, r.monoNow()
			return r.lastLux, true
		}
	}
	if r.lastLuxAt.IsZero() {
		return 0, false
	}
	if r.monoNow().Sub(r.lastLuxAt) > maxHoldAge {
		log.Print("no reading for over 30s, reporting the sensor as unavailable")
		r.lastLux, r.lastLuxAt = 0, time.Time{}
		return 0, false
	}
	return r.lastLux, true
}

type bridgeServer struct {
	readings      *readings
	eventInterval time.Duration
	pingInterval  time.Duration
}

func (s *bridgeServer) handler() http.Handler {
	ambientLight := getOnly(s.ambientLight)
	colorSensor := getOnly(noColorSensor)
	events := getOnly(s.events)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sensor/ambient_light":
			ambientLight(w, r)
		case "/sensor/ambient_color_temperature":
			colorSensor(w, r)
		case "/events":
			events(w, r)
		default:
			writeJSON(w, http.StatusNotFound, `{"detail":"Not Found"}`)
		}
	})
}

func getOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, `{"detail":"Method Not Allowed"}`)
			return
		}
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func formatLux(lux float64) string {
	return strconv.FormatFloat(lux, 'f', 1, 64)
}

func (s *bridgeServer) ambientLight(w http.ResponseWriter, _ *http.Request) {
	lux, ok := s.readings.lux()
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, `{"error":"no ambient light reading available"}`)
		return
	}
	value := formatLux(lux)
	writeJSON(w, http.StatusOK, `{"id":"sensor-ambient_light","state":"`+value+` lx","value":`+value+`}`)
}

func noColorSensor(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotFound, `{"error":"no color sensor"}`)
}

func (s *bridgeServer) events(w http.ResponseWriter, r *http.Request) {
	eventTicker := time.NewTicker(s.eventInterval)
	defer eventTicker.Stop()
	pingTicker := time.NewTicker(s.pingInterval)
	defer pingTicker.Stop()

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	if controller.Flush() != nil {
		return
	}

	write := func(frame string) error {
		if _, err := io.WriteString(w, frame); err != nil {
			return err
		}
		return controller.Flush()
	}
	sendState := func() error {
		lux, ok := s.readings.lux()
		if !ok {
			return nil
		}
		value := formatLux(lux)
		return write("event: state\r\ndata: " +
			`{"id": "sensor-ambient_light", "state": "` + value + ` lx", "value": ` + value + "}\r\n\r\n")
	}
	if sendState() != nil {
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-eventTicker.C:
			if sendState() != nil {
				return
			}
		case <-pingTicker.C:
			if write(": ping - "+s.readings.wallNow().UTC().Format(pingLayout)+"\r\n\r\n") != nil {
				return
			}
		}
	}
}
