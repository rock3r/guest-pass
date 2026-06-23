package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
	"github.com/rock3r/guest-pass/internal/token"
)

// camSlotCount is the addressable cam-slot pool size (cam 1..8, D-20/D-33). The optional
// host slot (D-18) is deferred to v1.1 (DEF-1) and never provisioned here.
const camSlotCount = 8

// slotCard is one slot as the read-only Sources tab renders it (EN-26). URL is the slot's
// current OBS source URL, always shown to the host — the raw token is stored alongside its
// hash (source_token_plain) so the host can copy the URL at any time; regenerating it rotates
// the token (D-22). OnAir is the three-state pill, defaulting to status-unavailable: the no-JS
// reference page (CONVENTIONS §3.1) carries no live WS, and D-24 forbids asserting on-air
// when unknown.
type slotCard struct {
	Title       string
	Label       string
	SlotID      string // DB id, for the regenerate form action (host-only, not a secret)
	URL         string
	Occupant    string
	HasOccupant bool
	OnAir       string
}

// sourcesTab renders the read-only Sources tab (AC-4 / EN-26). It idempotently provisions
// the host's slot pool (cam 1–8 + the shared screenshare slot) and shows each slot's current
// OBS source URL (the host can always copy it). Cards show slot + current occupant + the
// on-air pill and link to the greenroom People controls; there are no editable controls.
func (s *appServer) sourcesTab(w http.ResponseWriter, r *http.Request) {
	host, st, ok := s.ownedStream(w, r)
	if !ok {
		return
	}
	occ, err := s.slotOccupants(r.Context(), st.ID)
	if err != nil {
		http.Error(w, "could not load occupants", http.StatusInternalServerError)
		return
	}
	pool, err := s.ensureSlotPool(r.Context(), host.ID)
	if err != nil {
		http.Error(w, "could not provision slots", http.StatusInternalServerError)
		return
	}

	cards := make([]slotCard, 0, len(pool))
	for _, sl := range pool {
		if sl.Kind == store.SlotHost {
			continue // host slot deferred to v1.1 (DEF-1)
		}
		label := slotLabel(sl)
		c := slotCard{
			Title: slotTitle(sl), Label: label, SlotID: sl.ID, OnAir: "status-unavailable",
			URL: s.slotURL(label, sl.SourceTokenPlain),
		}
		if name, has := occ[sl.ID]; has {
			c.Occupant, c.HasOccupant = name, true
		}
		cards = append(cards, c)
	}
	live, otherLive := s.sessionState(r.Context(), host.ID, st.ID)
	s.rd.render(w, r, "streamdetail.html", pageData{
		Title: st.Title, Nav: "sources", Host: hostNav(host),
		CurrentStream: &navStream{ID: st.ID, Title: st.Title, IsLive: st.Status == "live"},
		Data: detailData{
			StreamID: st.ID, StreamTitle: st.Title, Tab: "sources",
			Slots: cards, SessionLive: live, OtherStreamLive: otherLive,
		},
	})
}

// ensureSlotPool idempotently provisions the host-global pool (cam 1–8 + screenshare, D-20)
// and returns the full pool. All candidate tokens are minted up front (the store never does
// crypto, EN-5; persisted hashed + plain), then EnsureSlotPool inserts the missing ones
// ATOMICALLY in one transaction.
func (s *appServer) ensureSlotPool(ctx context.Context, hostID string) ([]*store.Slot, error) {
	specs := make([]store.SlotSpec, 0, camSlotCount+1)
	add := func(kind string, idx *int64) error {
		raw, err := token.Mint()
		if err != nil {
			return err
		}
		specs = append(specs, store.SlotSpec{Kind: kind, Idx: idx, SourceTokenHash: s.hasher.Hash(raw), SourceTokenPlain: raw})
		return nil
	}
	for i := int64(1); i <= camSlotCount; i++ {
		idx := i
		if err := add(store.SlotCam, &idx); err != nil {
			return nil, err
		}
	}
	if err := add(store.SlotScreenshare, nil); err != nil {
		return nil, err
	}

	if _, err := s.store.EnsureSlotPool(ctx, hostID, specs); err != nil {
		return nil, err
	}
	pool, err := s.store.ListSlotsByHost(ctx, hostID)
	if err != nil {
		return nil, err
	}
	return pool, nil
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

// slotURL builds a slot's OBS source URL from its label and raw token.
func (s *appServer) slotURL(label, raw string) string {
	return s.baseURL + "/s/" + label + "?token=" + raw
}

// regenerateSlot rotates ONE slot's source token (D-22), invalidating the old OBS URL and
// tearing down any live /s/{slot} subscription with a token-rotated terminate. The fresh URL
// is shown on the redirected Sources tab (POST-redirect-GET).
func (s *appServer) regenerateSlot(w http.ResponseWriter, r *http.Request) {
	host, st, ok := s.ownedStream(w, r)
	if !ok {
		return
	}
	slot, err := s.store.GetSlot(r.Context(), chi.URLParam(r, "slotId"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && slot.HostID != host.ID) {
		http.Error(w, "slot not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "could not load slot", http.StatusInternalServerError)
		return
	}
	if _, err := s.rotateOne(r.Context(), host.ID, slot); err != nil {
		http.Error(w, "could not regenerate slot", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/app/streams/"+st.ID+"/sources", http.StatusSeeOther)
}

// regenerateAllSlots rotates EVERY slot's source token at once — the "my URLs leaked" panic
// button (D-22). It mints all the new tokens up front and rotates them in ONE transaction
// (RotateSlotTokens), so a mid-batch failure can never leave some slots on a mix of old and
// new tokens; then it tears down each live source. The fresh URLs show on the redirected tab.
func (s *appServer) regenerateAllSlots(w http.ResponseWriter, r *http.Request) {
	host, st, ok := s.ownedStream(w, r)
	if !ok {
		return
	}
	slots, err := s.store.ListSlotsByHost(r.Context(), host.ID)
	if err != nil {
		http.Error(w, "could not load slots", http.StatusInternalServerError)
		return
	}
	updatesByID := make(map[string]store.SlotTokenUpdate)
	labelByID := make(map[string]string)
	for _, sl := range slots {
		if sl.Kind == store.SlotHost {
			continue // host slot is unused this milestone (DEF-1)
		}
		raw, err := token.Mint()
		if err != nil {
			http.Error(w, "could not regenerate slots", http.StatusInternalServerError)
			return
		}
		label := slotLabel(sl)
		updatesByID[sl.ID] = store.SlotTokenUpdate{Hash: s.hasher.Hash(raw), Plain: raw}
		labelByID[sl.ID] = label
	}
	if err := s.store.RotateSlotTokens(r.Context(), updatesByID); err != nil {
		http.Error(w, "could not regenerate slots", http.StatusInternalServerError)
		return
	}
	// The hashes are all rotated (committed); now tear down each live source with token-rotated.
	if s.hub != nil {
		for _, label := range labelByID {
			s.hub.TerminateSourceIfLive(host.ID, "src-"+signaling.PeerID(label))
		}
	}
	http.Redirect(w, r, "/app/streams/"+st.ID+"/sources", http.StatusSeeOther)
}

// rotateOne mints a fresh token for slot, rotates the stored hash (old URL stops resolving,
// EN-5), tears down any live OBS source on that slot (token-rotated), and returns the slot
// label + the new full OBS URL to reveal once.
func (s *appServer) rotateOne(ctx context.Context, hostID string, slot *store.Slot) (string, error) {
	raw, err := token.Mint()
	if err != nil {
		return "", err
	}
	if err := s.store.RotateSlotToken(ctx, slot.ID, s.hasher.Hash(raw), raw); err != nil {
		return "", err
	}
	label := slotLabel(slot)
	// Tear down a live /s/{slot} subscription authenticated with the now-dead token, if any.
	if s.hub != nil {
		s.hub.TerminateSourceIfLive(hostID, "src-"+signaling.PeerID(label))
	}
	return label, nil
}
