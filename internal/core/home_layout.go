package core

import (
	"context"
	"time"
)

// The layered Home Designer (docs/owner-ui.md, "Home Designer";
// docs/personalization.md, "Layout"). Like Netflix, a Home is composed
// in three layers:
//
//  1. Global Home: the owner's template for everyone. A pool of rows with
//     a status each (always, eligible, off), anchored slots, position
//     bounds, frequency caps, schedules and page lengths per section and
//     device.
//  2. Personalized: within those constraints the engine picks and orders
//     the eligible rows for each viewer and context.
//  3. Per-viewer overrides: the owner can override any Global setting for
//     one viewer; an override replaces the Global policy for that row (or
//     that page setting) and "reset to global" removes it.

// Row policy statuses.
const (
	RowAlways   = "always"   // on the page whenever it has titles
	RowEligible = "eligible" // in the pool: personalization decides (default)
	RowOff      = "off"      // never shown
)

// Layers, for HomeRow.Layer and HomeRow.PolicyLayer.
const (
	LayerGlobal       = "global"
	LayerPersonalized = "personalized"
	LayerOverride     = "override"
)

// RowPolicy is the owner's policy for one row (or a family of rows) on a
// page. Row matches a row's kind ("short-on-time", "collection:<id>"),
// its hub id ("home.because.123", with or without the screen prefix) or a
// glob over either ("because.*", "collection:*"). A more specific match
// wins within a layer (hub id, then kind, then glob); a viewer's override
// always beats the Global policy.
type RowPolicy struct {
	Row    string `json:"row"`
	Status string `json:"status"` // RowAlways, RowEligible, RowOff
	// Slot anchors the row to a 1-based position on the page (1 = top).
	// An anchored row is always shown when it has titles.
	Slot int `json:"slot,omitempty"`
	// MinSlot: never above this slot. MaxSlot: never below this slot.
	// Bounds constrain where personalization may put the row when it is
	// on the page; combine with Always to guarantee it is.
	MinSlot int `json:"minSlot,omitempty"`
	MaxSlot int `json:"maxSlot,omitempty"`
	// Priority multiplies the row's predicted engagement before ranking
	// (0 = 1; 0.25 … 4).
	Priority float64 `json:"priority,omitempty"`
	// MaxPerPage caps how many rows this policy matches on one page
	// (Because You Watched: 3 by default). 0 = the engine's default.
	MaxPerPage int `json:"maxPerPage,omitempty"`
	// MaxDaysPerWeek is a frequency cap: the row appears for a viewer on
	// at most this many of any 7 days (it stays for the rest of a day it
	// was shown on). 0 = no cap.
	MaxDaysPerWeek int `json:"maxDaysPerWeek,omitempty"`
	// When limits the row to a schedule (days, hours, dayparts, devices,
	// months, dates). Outside it the row is off.
	When *LayoutWhen `json:"when,omitempty"`
}

// LayoutPage is one layer's settings for a page: Home ("home") or a
// library section ("movies", "shows").
type LayoutPage struct {
	Section string `json:"section"`
	// MaxRows is the page length: rows the engine arranges, Plex's own
	// rows not counted. 0 = inherit (Global: the engine default).
	MaxRows int `json:"maxRows,omitempty"`
	// DeviceMaxRows sets the page length per device class (DeviceTV …);
	// it beats MaxRows of the same layer.
	DeviceMaxRows map[string]int `json:"deviceMaxRows,omitempty"`
	Rows          []RowPolicy    `json:"rows"`
}

// ViewerLayout is the owner's overrides for one viewer.
type ViewerLayout struct {
	UserID int64        `json:"userId"`
	Pages  []LayoutPage `json:"pages"`
}

// HomeLayout is the Home Designer document: the Global template plus
// per-viewer overrides.
type HomeLayout struct {
	Global    []LayoutPage   `json:"global"`
	Viewers   []ViewerLayout `json:"viewers"`
	UpdatedAt time.Time      `json:"updatedAt,omitzero"`
	// Notes are server-written remarks (how legacy layout rules were
	// converted); they are cleared by the next save.
	Notes []string `json:"notes,omitempty"`
}

// LayoutImpact is how a draft layout changes one viewer's page compared
// with the saved layout, in one context.
type LayoutImpact struct {
	UserID int64  `json:"viewerId"`
	Title  string `json:"title"`
	// Overrides counts the viewer's row and page overrides on this page.
	Overrides int  `json:"overrides"`
	Changed   bool `json:"changed"`
	// Added and Removed are row titles that appear or disappear; Moved
	// counts rows that stay but change position.
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	Moved   int      `json:"moved"`
}

// CollectionRowLookup is optionally implemented by the CollectionRows
// source: it serves specific owner collections as rows for a viewer
// regardless of their Home/section targeting, because the owner placed
// them on a page in the Home Designer. Disabled collections are skipped.
// With ignoreAudience false the collection's audience (Targets.Users)
// still applies (Global placements); a per-viewer placement passes true.
type CollectionRowLookup interface {
	CollectionRowsByID(ctx context.Context, user *User, hc HubContext, ids []string, ignoreAudience bool) ([]CollectionRow, error)
}
