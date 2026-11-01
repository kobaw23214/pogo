package main

import (
	"encoding/json"
	"os"
)

// tiwebStats answers malware.tiweb.stats over the packaged aggregate
// snapshot: the counts, spans, top countries and yearly series the threat
// intelligence questions need, precomputed at package build time so the
// provider stays a pure lookup.

type countryCount struct {
	Country string `json:"country"`
	Count   int    `json:"count"`
}

type yearCount struct {
	Year  int `json:"year"`
	Count int `json:"count"`
}

type tiwebSnapshot struct {
	Note string `json:"note"`
	URLhaus struct {
		Rows         int            `json:"rows"`
		GeoRows      int            `json:"geoRows"`
		FirstSeen    string         `json:"firstSeen"`
		LastSeen     string         `json:"lastSeen"`
		TopCountries []countryCount `json:"topCountries"`
		Monthly      []monthCount   `json:"monthly"`
	} `json:"urlhaus"`
	Ransomwatch struct {
		Rows      int         `json:"rows"`
		Groups    int         `json:"groups"`
		FirstSeen string      `json:"firstSeen"`
		LastSeen  string      `json:"lastSeen"`
		Yearly    []yearCount `json:"yearly"`
	} `json:"ransomwatch"`
	IOC struct {
		Domains int `json:"domains"`
		IPs     int `json:"ips"`
	} `json:"ioc"`
}

type monthCount struct {
	Month string `json:"month"`
	Count int    `json:"count"`
}

type tiwebStatsInput struct {
	TopN int `json:"topN,omitempty"`
}

func tiwebStats(path string, rawInput json.RawMessage) (json.RawMessage, error) {
	var in tiwebStatsInput
	if len(rawInput) > 0 {
		if err := json.Unmarshal(rawInput, &in); err != nil {
			return nil, &toolError{code: "INVALID_INPUT", message: err.Error()}
		}
	}
	topN := in.TopN
	if topN <= 0 {
		topN = 10
	}
	if topN > 50 {
		topN = 50
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &toolError{code: "ASSET_MISSING", message: err.Error()}
	}
	var snap tiwebSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, &toolError{code: "ASSET_INVALID", message: err.Error()}
	}
	if len(snap.URLhaus.TopCountries) > topN {
		snap.URLhaus.TopCountries = snap.URLhaus.TopCountries[:topN]
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	return raw, nil
}
