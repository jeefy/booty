package controller

import (
	"fmt"
	"slices"
	"strings"

	"github.com/jeefy/booty/pkg/versions"
)

// compactReleases bounds the release history in state.json without
// forgetting a verdict. A good (or skipped) record whose files are gone,
// that is neither current nor lastGood and never went through TIMEOUT or
// quarantine (no report), is folded: its per-host lists go (Healthy,
// FailedOn), version/state/since/attempts/class stay. Quarantined and
// TIMEOUT records are never touched. Per OS, the good records without
// files are capped at maxGoodHistory: beyond it the oldest folded ones are
// dropped, the only deletion the controller does to its history. The work
// is keyed on what can change the outcome (the record list, the cached
// list, current and lastGood), so a quiet tick costs a string compare.
func (c *Controller) compactReleases() {
	for _, osName := range osNames() {
		st := c.state.OS[osName]
		if st == nil || len(st.Releases) == 0 {
			continue
		}
		cur, lastGood, cached := c.opts.Fleet.Current(osName), c.opts.Fleet.LastGood(osName), c.opts.Fleet.Cached(osName)
		fingerprint := func() string {
			return fmt.Sprintf("%d|%s|%s|%s", len(st.Releases), cur, lastGood, strings.Join(cached, ","))
		}
		if c.compacted[osName] == fingerprint() {
			continue
		}
		c.dirty = true
		var history []*Release
		for _, r := range st.Releases {
			if !r.settled() || slices.Contains(cached, r.Version) || r.Version == cur || r.Version == lastGood {
				continue
			}
			history = append(history, r)
			if r.Report == "" {
				r.fold()
			}
		}
		if len(history) > maxGoodHistory {
			c.dropOldestFolded(osName, history)
		}
		c.compacted[osName] = fingerprint()
	}
}

// dropOldestFolded deletes the oldest folded records of history until at
// most maxGoodHistory remain; records with a report are skipped.
func (c *Controller) dropOldestFolded(osName string, history []*Release) {
	slices.SortFunc(history, func(a, b *Release) int {
		if d := a.Since.Compare(b.Since); d != 0 {
			return d
		}
		return versions.CompareVersions(a.Version, b.Version)
	})
	dropped := 0
	for _, r := range history {
		if len(history)-dropped <= maxGoodHistory {
			break
		}
		if r.Report != "" {
			continue
		}
		delete(c.state.OS[osName].Releases, r.Version)
		dropped++
	}
	if dropped > 0 {
		c.event(EventRelease, osName, "", "", fmt.Sprintf("release history compacted: %d oldest good record(s) without files dropped (%d kept)", dropped, maxGoodHistory))
	}
}

// fold drops what a good release without files no longer needs: which
// hosts passed on it and which one blamed it.
func (r *Release) fold() {
	r.Healthy, r.FailedOn = nil, ""
}
