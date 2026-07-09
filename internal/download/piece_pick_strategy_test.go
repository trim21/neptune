// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"testing"
)

func TestPiecePickStrategy_DefaultIsRarestFirst(t *testing.T) {
	if defaultStrategy("") != StrategyRarestFirst {
		t.Fatal("empty config should default to rarest-first")
	}
	if defaultStrategy("unknown") != StrategyRarestFirst {
		t.Fatal("unknown value should default to rarest-first")
	}
	if defaultStrategy("sequential") != StrategySequential {
		t.Fatal("sequential should be recognized")
	}
	if defaultStrategy("rarest-first") != StrategyRarestFirst {
		t.Fatal("rarest-first should be recognized")
	}
}

func TestPiecePickStrategy_String(t *testing.T) {
	if StrategyRarestFirst.String() != "rarest-first" {
		t.Fatal("rarest-first string")
	}
	if StrategySequential.String() != "sequential" {
		t.Fatal("sequential string")
	}
	if PiecePickStrategy(255).String() != "<invalid>" {
		t.Fatal("unknown should be <invalid>")
	}
}

func TestPiecePickStrategy_FromString(t *testing.T) {
	for _, s := range []string{"rarest-first", "sequential"} {
		v, err := PiecePickStrategyFromString(s)
		if err != nil {
			t.Fatalf("failed to parse %q: %v", s, err)
		}
		if v.String() != s {
			t.Fatalf("roundtrip failed: %s -> %s", s, v.String())
		}
	}

	_, err := PiecePickStrategyFromString("unknown")
	if err == nil {
		t.Fatal("should fail for unknown value")
	}
}
