// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAnnounceToScrape(t *testing.T) {
	tests := []struct {
		name     string
		announce string
		wantURL  string
		wantOK   bool
	}{
		{
			name:     "basic announce",
			announce: "http://tracker.example.com/announce",
			wantURL:  "http://tracker.example.com/scrape",
			wantOK:   true,
		},
		{
			name:     "announce with query params",
			announce: "http://tracker.example.com/announce?passkey=abc123",
			wantURL:  "http://tracker.example.com/scrape?passkey=abc123",
			wantOK:   true,
		},
		{
			name:     "announce with port",
			announce: "http://tracker.example.com:8080/announce",
			wantURL:  "http://tracker.example.com:8080/scrape",
			wantOK:   true,
		},
		{
			name:     "non-scrapeable URL",
			announce: "http://tracker.example.com/update",
			wantURL:  "",
			wantOK:   false,
		},
		{
			name:     "invalid URL",
			announce: "://invalid",
			wantURL:  "",
			wantOK:   false,
		},
		{
			name:     "announce with trailing slash",
			announce: "http://tracker.example.com/announce/",
			wantURL:  "",
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, gotOK := announceToScrape(tt.announce)
			assert.Equal(t, tt.wantOK, gotOK)
			assert.Equal(t, tt.wantURL, gotURL)
		})
	}
}
