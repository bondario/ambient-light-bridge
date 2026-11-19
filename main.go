package main

import (
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"runtime/debug"
	"time"
)

const (
	sampleInterval = 2 * time.Second
	hangTimeout    = 30 * time.Second
)

type sampler struct {
	readings    *readings
	read        func() (sensorReading, error)
	exit        func(int)
	hangTimeout time.Duration
	tried       bool
	hadReading  bool
	lastSource  source
	lastMatches int
}

func (s *sampler) sample() {
	timer := time.AfterFunc(s.hangTimeout, func() {
		log.Printf("sensor read timed out after %s", s.hangTimeout)
		s.exit(1)
	})
	reading, err := s.read()
	if !timer.Stop() {
		return
	}
	if err != nil {
		if !s.tried || s.hadReading {
			log.Printf("no reading: %v", err)
		}
	} else {
		lux := math.Round(reading.lux*10) / 10
		s.readings.store(lux)
		if !s.hadReading || reading.source != s.lastSource {
			log.Printf("reading: %s lx from %s", formatLux(lux), reading.source)
			s.lastSource = reading.source
		}
		if reading.matches > 1 && reading.matches != s.lastMatches {
			log.Printf("warning: %d ambient light sensors match, using %s", reading.matches, reading.source)
		}
		s.lastMatches = reading.matches
	}
	s.tried = true
	s.hadReading = err == nil
}

func (s *sampler) run() {
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()
	s.sample()
	for range ticker.C {
		s.sample()
	}
}

func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		return info.Main.Version
	}
	return "unknown"
}

func main() {
	addr := flag.String("addr", "", "listen address (host:port)")
	flag.Parse()
	if *addr == "" {
		fmt.Fprintln(os.Stderr, "error: -addr flag is required")
		os.Exit(2)
	}
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "error: unexpected positional arguments")
		os.Exit(2)
	}

	rs := &readings{wallNow: time.Now, monoNow: time.Now}
	smp := &sampler{readings: rs, read: readLux, exit: os.Exit, hangTimeout: hangTimeout}
	bridge := &bridgeServer{readings: rs, eventInterval: 2 * time.Second, pingInterval: 15 * time.Second}
	server := &http.Server{
		Addr:                         *addr,
		Handler:                      bridge.handler(),
		ReadHeaderTimeout:            5 * time.Second,
		DisableGeneralOptionsHandler: true,
	}
	log.Printf("ambient-light-bridge %s listening on %s", buildVersion(), *addr)
	go smp.run()
	log.Fatal(server.ListenAndServe())
}
