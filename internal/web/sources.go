package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rock3r/guest-pass/internal/store"
	"github.com/rock3r/guest-pass/internal/token"
)

// camSlotCount is the addressable cam-slot pool size (cam 1..8, D-20/D-33). The optional
// host slot (D-18) is deferred to v1.1 (DEF-1) and never provisioned here.
const camSlotCount = 8

// slotCard is one slot as the read-only Sources tab renders it (EN-26). URL is set ONLY
// when the slot was freshly minted on this load — the source token is stored hashed (EN-5),
// so it can be surfaced exactly once, at provisioning; re-revealing it is a regenerate
// (D-22, PR-5). OnAir is the three-state pill, defaulting to status-unavailable: the no-JS
// reference page (CONVENTIONS §3.1) carries no live WS, and D-24 forbids asserting on-air
// when unknown.
type slotCard struct {
	Title       string
	Label       string
	URL         string
	Revealed    bool
	Occupant    string
	HasOccupant bool
	OnAir       string
}

// sourcesTab renders the read-only Sources tab (AC-4 / EN-26). It idempotently provisions
// the host's slot pool (cam 1–8 + the shared screenshare slot) and reveals each
// freshly-minted slot's permanent OBS URL once. Cards show slot + current occupant + the
// on-air pill and link to the greenroom People controls; there are no editable controls.
func (s *appServer) sourcesTab(w http.ResponseWriter, r *http.Request) {
	host, st, ok := s.ownedStream(w, r)
	if !ok {
		return
	}
	// Load occupants BEFORE provisioning so a transient read failure errors out before any
	// slot is minted — otherwise the freshly-minted (and thus revealable) tokens would be
	// lost to the hashed store with the response never rendered.
	occ, err := s.slotOccupants(r.Context(), st.ID)
	if err != nil {
		http.Error(w, "could not load occupants", http.StatusInternalServerError)
		return
	}
	pool, revealed, err := s.ensureSlotPool(r.Context(), host.ID)
	if err != nil {
		http.Error(w, "could not provision slots", http.StatusInternalServerError)
		return
	}
	cards := make([]slotCard, 0, len(pool))
	var revealLines []string
	for _, sl := range pool {
		if sl.Kind == store.SlotHost {
			continue // host slot deferred to v1.1 (DEF-1)
		}
		label := slotLabel(sl)
		c := slotCard{Title: slotTitle(sl), Label: label, OnAir: "status-unavailable"}
		if name, has := occ[sl.ID]; has {
			c.Occupant, c.HasOccupant = name, true
		}
		if raw, ok := revealed[label]; ok {
			c.URL = s.baseURL + "/s/" + label + "?token=" + raw
			c.Revealed = true
			revealLines = append(revealLines, c.URL)
		}
		cards = append(cards, c)
	}
	s.rd.render(w, r, "streamdetail.html", pageData{
		Title: st.Title, Nav: "dashboard", Host: &navHost{Name: host.Name},
		Data: detailData{
			StreamID: st.ID, StreamTitle: st.Title, Tab: "sources",
			Slots: cards, RevealAll: strings.Join(revealLines, "\n"), HasReveal: len(revealLines) > 0,
		},
	})
}

// ensureSlotPool idempotently provisions the host-global pool (cam 1–8 + screenshare, D-20)
// and returns the full pool plus the raw tokens for the slots minted on THIS call, keyed by
// slot label, so the handler can reveal those URLs once. All candidate tokens are minted up
// front (the store never does crypto, EN-5; persisted hashed), then EnsureSlotPool inserts
// the missing ones ATOMICALLY in one transaction — so a concurrent first open reveals the
// full set exactly once rather than a partial split across the two responses.
func (s *appServer) ensureSlotPool(ctx context.Context, hostID string) ([]*store.Slot, map[string]string, error) {
	labels := make([]string, 0, camSlotCount+1)
	raws := make([]string, 0, camSlotCount+1)
	specs := make([]store.SlotSpec, 0, camSlotCount+1)
	add := func(kind string, idx *int64, label string) error {
		raw, err := token.Mint()
		if err != nil {
			return err
		}
		labels = append(labels, label)
		raws = append(raws, raw)
		specs = append(specs, store.SlotSpec{Kind: kind, Idx: idx, SourceTokenHash: s.hasher.Hash(raw)})
		return nil
	}
	for i := int64(1); i <= camSlotCount; i++ {
		idx := i
		if err := add(store.SlotCam, &idx, fmt.Sprintf("cam-%d", i)); err != nil {
			return nil, nil, err
		}
	}
	if err := add(store.SlotScreenshare, nil, "screen"); err != nil {
		return nil, nil, err
	}

	inserted, err := s.store.EnsureSlotPool(ctx, hostID, specs)
	if err != nil {
		return nil, nil, err
	}
	revealed := make(map[string]string, len(inserted))
	for i, ins := range inserted {
		if ins {
			revealed[labels[i]] = raws[i]
		}
	}
	pool, err := s.store.ListSlotsByHost(ctx, hostID)
	if err != nil {
		return nil, nil, err
	}
	return pool, revealed, nil
}

// slotOccupants maps each slot id to the display name of the pass in streamID currently
// bound to it (the active occupant; revoked/expired passes are ignored).
func (s *appServer) slotOccupants(ctx context.Context, streamID string) (map[string]string, error) {
	passes, err := s.store.ListPassesByStream(ctx, streamID)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	occ := make(map[string]string)
	for _, p := range passes {
		// A revoked pass, or one past its expiry deadline (even if its status hasn't been
		// swept to "expired" yet), is no longer the slot's occupant — same deadline check the
		// Invites display uses (passExpired), so the two tabs stay consistent.
		if p.SlotID == nil || p.Status == store.PassRevoked || passExpired(p, now) {
			continue
		}
		occ[*p.SlotID] = occupantLabel(p)
	}
	return occ, nil
}

func occupantLabel(p *store.Pass) string {
	if p.Name != nil && *p.Name != "" {
		return *p.Name
	}
	if p.Email != nil && *p.Email != "" {
		return *p.Email
	}
	return "Guest"
}

// slotLabel is the opaque slot id used in the OBS URL path and signaling (RF-26 grammar:
// cam-1..cam-8 | screen | host).
func slotLabel(sl *store.Slot) string {
	switch sl.Kind {
	case store.SlotCam:
		if sl.Idx != nil {
			return fmt.Sprintf("cam-%d", *sl.Idx)
		}
		return "cam"
	case store.SlotScreenshare:
		return "screen"
	case store.SlotHost:
		return "host"
	default:
		return sl.Kind
	}
}

// slotTitle is the human label for a slot card.
func slotTitle(sl *store.Slot) string {
	switch sl.Kind {
	case store.SlotCam:
		if sl.Idx != nil {
			return fmt.Sprintf("Cam %d", *sl.Idx)
		}
		return "Cam"
	case store.SlotScreenshare:
		return "Screenshare"
	case store.SlotHost:
		return "Host"
	default:
		return sl.Kind
	}
}
