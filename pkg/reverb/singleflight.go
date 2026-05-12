package reverb

import (
	"context"
	"encoding/hex"
	"sync/atomic"

	"github.com/nobelk/reverb/internal/hashutil"
	"github.com/nobelk/reverb/pkg/normalize"
)

// FillFunc produces a StoreRequest for a cache miss. It is invoked by
// LookupOrCall when no cached entry exists. The returned StoreRequest is
// written back into the cache before LookupOrCall returns.
type FillFunc func(ctx context.Context) (StoreRequest, error)

// LookupOrCall checks the cache and, on miss, invokes fill exactly once even
// when many callers race for the same prompt. Concurrent identical misses are
// coalesced via golang.org/x/sync/singleflight, keyed by the same SHA-256
// prompt hash the exact tier uses — so two callers with byte-identical
// normalized prompts (and namespace/model) share a single fill.
//
// Returns the cache entry, a boolean that is true when the result came from a
// pre-existing cache hit and false when this call (or the leader of a
// coalesced flight) populated the entry, and any error from fill or the
// underlying Store.
//
// If fill returns an error, every coalesced caller receives the same error
// and no entry is stored. The flight is per-process; cluster-wide coalescing
// is roadmap §3.6.
func (c *Client) LookupOrCall(ctx context.Context, req LookupRequest, fill FillFunc) (*LookupResponse, bool, error) {
	if fill == nil {
		// Without a fill, this is just Lookup — degrade gracefully.
		resp, err := c.Lookup(ctx, req)
		if err != nil {
			return nil, false, err
		}
		return resp, resp.Hit, nil
	}

	// Fast path: cache hit returns immediately, no flight needed.
	resp, err := c.Lookup(ctx, req)
	if err != nil {
		return nil, false, err
	}
	if resp.Hit {
		return resp, true, nil
	}

	ns := req.Namespace
	if ns == "" {
		ns = c.cfg.DefaultNamespace
	}
	normalized := normalize.Normalize(req.Prompt)
	hash := hashutil.PromptHash(ns, normalized, req.ModelID)
	key := hex.EncodeToString(hash[:])

	// Track whether this caller is the leader so followers can be counted
	// as coalesced. singleflight.Do reports `shared==true` to *all* callers
	// in a coalesced flight (leader included), so the bool alone cannot
	// distinguish leader from follower.
	var leader atomic.Bool

	v, doErr, _ := c.sfGroup.Do(key, func() (any, error) {
		leader.Store(true)
		storeReq, fillErr := fill(ctx)
		if fillErr != nil {
			return nil, fillErr
		}
		// Honor the caller's namespace/model on the lookup request even if
		// fill returned an empty/different one — the cache key is determined
		// by what we just looked up.
		if storeReq.Namespace == "" {
			storeReq.Namespace = req.Namespace
		}
		if storeReq.Prompt == "" {
			storeReq.Prompt = req.Prompt
		}
		if storeReq.ModelID == "" {
			storeReq.ModelID = req.ModelID
		}
		entry, storeErr := c.Store(ctx, storeReq)
		if storeErr != nil {
			return nil, storeErr
		}
		return &LookupResponse{
			Hit:        false,
			Tier:       "",
			Similarity: 0,
			Entry:      entry,
		}, nil
	})
	if doErr != nil {
		return nil, false, doErr
	}

	if !leader.Load() {
		c.collector.SingleflightCoalesced.Add(1)
		if c.prom != nil {
			c.prom.SingleflightCoalescedTotal.Inc()
		}
	}

	out, _ := v.(*LookupResponse)
	return out, false, nil
}

