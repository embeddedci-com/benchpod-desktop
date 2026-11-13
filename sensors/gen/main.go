// Command gen refreshes the embedded copy of the sensor library (sensors/embedded)
// from a checkout of github.com/embeddedci-com/benchpod-sensors, and writes its library.json.
//
//	go run ./sensors/gen -src ../benchpod-sensors -version 0.1.0
//
// The version must be the release tag the copy matches (without the "v"): the server serves the
// embedded copy for that version without fetching it. The same index format is what each release
// tarball carries at its root (see package sensorlib).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	sensorlib "github.com/embeddedci-com/benchpod-desktop/sensors"
)

func main() {
	src := flag.String("src", "../benchpod-sensors", "checkout of the benchpod-sensors repository")
	out := flag.String("out", "sensors/embedded", "directory to write the embedded copy to")
	version := flag.String("version", "", "library version (X.Y.Z) the copy matches")
	flag.Parse()
	v, ok := sensorlib.NormalizeVersion(*version)
	if !ok || v == "" || v == sensorlib.Latest {
		log.Fatalf("-version must be X.Y.Z, got %q", *version)
	}
	idx, files, err := sensorlib.BuildIndex(os.DirFS(*src), v)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.RemoveAll(*out); err != nil {
		log.Fatal(err)
	}
	for p, data := range files {
		dst := filepath.Join(*out, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			log.Fatal(err)
		}
	}
	js, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*out, sensorlib.IndexFile), append(js, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %d entries, version %s, to %s\n", len(idx.Entries), v, *out)
}
