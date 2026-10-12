// SPDX-License-Identifier: GPL-3.0-or-later

//go:build race

package store

// raceEnabled reports whether the tests run under the race detector,
// which makes the property test about five times slower.
const raceEnabled = true
