// Package update implements the SHN Kit's launch-time update check: a
// single best-effort GET against a GitHub
// "latest release" feed, compared by semver against the running Kit's own
// version.
//
// This package is deliberately narrow: Check has no notion of "dev build,"
// no retry/backoff, and never logs anything itself. Every decision about
// WHETHER to check (dev builds skip it entirely) and WHAT to do with a
// failure (log and discard — an update check is advisory only and must
// never affect boot) belongs to the caller (kit/cmd/shnkitd's main), so this
// package stays a pure, hermetically-testable function.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// Info is the result of a launch-time update check. Available reports
// whether the feed's latest release is newer than the version Check was
// asked to compare against; Latest is the feed's tag_name and URL its
// html_url release page — both left "" when Available is false (there is
// nothing newer to point at).
type Info struct {
	Available bool   `json:"available"`
	Latest    string `json:"latest,omitempty"`
	URL       string `json:"url,omitempty"`
}

// releaseFeed is the subset of a GitHub "get the latest release" API
// response (https://api.github.com/repos/{owner}/{repo}/releases/latest)
// this package needs.
type releaseFeed struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

// Check GETs feedURL — a GitHub "latest release" endpoint — and compares its
// tag_name against current via semver (major.minor.patch; both "v"-prefixed
// and bare forms are accepted on either side). The response body is read
// through io.LimitReader(resp.Body, shnsdk.MaxResponseBytes) — the same cap
// this module already applies at bootstrap/verify.go, byo/byo.go, and
// byo/browse.go — so a misbehaving or hostile feed can't be read unbounded
// into memory.
//
// EVERY failure — request construction, network error, non-2xx status,
// malformed JSON, an empty tag_name, or either version failing to parse —
// returns a zero Info alongside a non-nil, descriptive error. Check itself
// never logs and never treats current == "0.0.0-dev" (the ldflags dev
// sentinel, kit/cmd/shnkitd's kitVersion default) specially: skipping the
// check for dev builds is the CALLER's decision, made before Check is ever
// invoked, not something this function infers from its input.
//
// A nil hc defaults to http.DefaultClient (mirrors bootstrap.Verify's own
// nil-client guard).
func Check(ctx context.Context, hc *http.Client, feedURL, current string) (Info, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return Info{}, fmt.Errorf("update: build request for %s: %w", feedURL, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := hc.Do(req)
	if err != nil {
		return Info{}, fmt.Errorf("update: fetch %s: %w", feedURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Info{}, fmt.Errorf("update: %s: HTTP %d", feedURL, resp.StatusCode)
	}

	var rf releaseFeed
	if err := json.NewDecoder(io.LimitReader(resp.Body, shnsdk.MaxResponseBytes)).Decode(&rf); err != nil {
		return Info{}, fmt.Errorf("update: decode %s response: %w", feedURL, err)
	}
	if rf.TagName == "" {
		return Info{}, fmt.Errorf("update: %s: response has an empty tag_name", feedURL)
	}

	newer, err := isNewer(rf.TagName, current)
	if err != nil {
		return Info{}, fmt.Errorf("update: compare feed version %q against current %q: %w", rf.TagName, current, err)
	}
	if !newer {
		return Info{Available: false}, nil
	}
	return Info{Available: true, Latest: rf.TagName, URL: rf.HTMLURL}, nil
}

// isNewer reports whether latest > current under a plain major.minor.patch
// semver compare (no pre-release/build-metadata precedence rules — Kit
// releases don't use them).
func isNewer(latest, current string) (bool, error) {
	l, err := parseSemver(latest)
	if err != nil {
		return false, fmt.Errorf("latest version: %w", err)
	}
	c, err := parseSemver(current)
	if err != nil {
		return false, fmt.Errorf("current version: %w", err)
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i], nil
		}
	}
	return false, nil
}

// parseSemver parses v (an optional "v" prefix, then major.minor.patch) into
// its three integer components. Each component's value is its LEADING run of
// digits — "0-dev" parses as 0 — so an unusual current version (like the
// ldflags dev sentinel "0.0.0-dev", were a caller ever to pass it here
// anyway) still parses instead of erroring on the non-numeric suffix; a
// component with no leading digits at all is a genuine parse failure.
func parseSemver(v string) ([3]int, error) {
	var out [3]int
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return out, fmt.Errorf("not a major.minor.patch semver: %q", v)
	}
	for i, p := range parts {
		n, err := leadingInt(p)
		if err != nil {
			return out, fmt.Errorf("version component %q in %q: %w", p, v, err)
		}
		out[i] = n
	}
	return out, nil
}

// leadingInt returns the integer formed by s's leading run of ASCII digits.
// An error if s has no leading digit at all.
func leadingInt(s string) (int, error) {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, fmt.Errorf("no leading digits in %q", s)
	}
	return strconv.Atoi(s[:end])
}
