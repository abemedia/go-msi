// Package olepstest provides utilities for testing the oleps package.
package olepstest

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/abemedia/go-msi/internal/guid"
	"github.com/abemedia/go-msi/internal/oleps"
)

type fixture struct {
	Version          uint16 `json:"version"`
	SystemIdentifier uint32 `json:"systemIdentifier"`
	CLSID            string `json:"clsid"`
	Sets             []struct {
		FMTID      string `json:"fmtid"`
		Properties []struct {
			ID    uint32 `json:"id"`
			Type  string `json:"type"`
			Value any    `json:"value"`
		} `json:"properties"`
	} `json:"sets"`
}

// LoadFixtures reads JSON-encoded fixtures from path and returns each entry
// as an oleps stream.
func LoadFixtures(path string) (map[string]oleps.PropertySetStream, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]fixture
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	out := make(map[string]oleps.PropertySetStream, len(m))
	for name, f := range m {
		var clsid [16]byte
		if f.CLSID != "" {
			clsid, err = guid.Parse(f.CLSID)
			if err != nil {
				return nil, fmt.Errorf("%s: clsid: %w", name, err)
			}
		}
		s := oleps.PropertySetStream{
			Version:          f.Version,
			SystemIdentifier: f.SystemIdentifier,
			CLSID:            clsid,
			PropertySets:     make([]oleps.PropertySet, len(f.Sets)),
		}
		for i, set := range f.Sets {
			fmtid, err := guid.Parse(set.FMTID)
			if err != nil {
				return nil, fmt.Errorf("%s: sets[%d].fmtid: %w", name, i, err)
			}
			s.PropertySets[i] = oleps.PropertySet{FMTID: fmtid, Properties: make([]oleps.Property, len(set.Properties))}
			for j, p := range set.Properties {
				var v oleps.Value
				switch p.Type {
				case "i2":
					v = oleps.I2(int16(int64(p.Value.(float64))))
				case "i4":
					v = oleps.I4(int32(int64(p.Value.(float64))))
				case "ui4":
					v = oleps.UI4(uint32(int64(p.Value.(float64))))
				case "lpstr":
					v = oleps.LPSTR(p.Value.(string))
				case "filetime":
					ts, err := time.Parse(time.RFC3339, p.Value.(string))
					if err != nil {
						return nil, fmt.Errorf("%s: filetime %q: %w", name, p.Value, err)
					}
					v = oleps.FileTime(ts)
				default:
					return nil, fmt.Errorf("%s: unknown property type %q", name, p.Type)
				}
				s.PropertySets[i].Properties[j] = oleps.Property{ID: p.ID, Value: v}
			}
		}
		out[name] = s
	}
	return out, nil
}
