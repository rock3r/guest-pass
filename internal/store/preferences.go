package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// HostPreferences are the host-owned defaults applied to subsequently created streams. The TURN
// secret is ciphertext only; callers must never return it to HTML, logs, or account exports.
type HostPreferences struct {
	HostID                    string
	Timezone                  string
	YouTubeChannel            string
	TwitchChannel             string
	DefaultChannelPlatform    string // "" | youtube | twitch
	MaxRes                    int64
	MaxFPS                    int64
	MaxBitrateKbps            int64
	CustomTURNEnabled         bool
	CustomTURNURL             string
	CustomTURNSecretEncrypted string
}

func defaultHostPreferences(hostID string) *HostPreferences {
	return &HostPreferences{
		HostID:         hostID,
		Timezone:       "UTC",
		MaxRes:         DefaultMaxRes,
		MaxFPS:         DefaultMaxFPS,
		MaxBitrateKbps: DefaultMaxBitrateKbps,
	}
}

// GetHostPreferences returns the stored host preferences, or the application defaults for a host
// that has not customized them yet. A missing row is intentional and does not cause a write.
func (s *Store) GetHostPreferences(ctx context.Context, hostID string) (*HostPreferences, error) {
	row := s.reader.QueryRowContext(ctx, `SELECT host_id, timezone, youtube_channel, twitch_channel,
        default_channel_platform, max_res, max_fps, max_bitrate_kbps, custom_turn_enabled,
        custom_turn_url, custom_turn_secret_encrypted
        FROM host_preferences WHERE host_id = ?`, hostID)
	p, err := scanHostPreferences(row)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultHostPreferences(hostID), nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting host preferences: %w", err)
	}
	return p, nil
}

// SetHostPreferences replaces the complete preference set for a host. The fixed SQL column list
// prevents user-controlled identifiers; validation belongs to the web boundary before persistence.
func (s *Store) SetHostPreferences(ctx context.Context, p HostPreferences) error {
	_, err := s.writer.ExecContext(ctx, `INSERT INTO host_preferences (
        host_id, timezone, youtube_channel, twitch_channel, default_channel_platform,
        max_res, max_fps, max_bitrate_kbps, custom_turn_enabled, custom_turn_url,
        custom_turn_secret_encrypted
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(host_id) DO UPDATE SET
        timezone = excluded.timezone,
        youtube_channel = excluded.youtube_channel,
        twitch_channel = excluded.twitch_channel,
        default_channel_platform = excluded.default_channel_platform,
        max_res = excluded.max_res,
        max_fps = excluded.max_fps,
        max_bitrate_kbps = excluded.max_bitrate_kbps,
        custom_turn_enabled = excluded.custom_turn_enabled,
        custom_turn_url = excluded.custom_turn_url,
        custom_turn_secret_encrypted = excluded.custom_turn_secret_encrypted`,
		p.HostID, p.Timezone, p.YouTubeChannel, p.TwitchChannel, p.DefaultChannelPlatform,
		p.MaxRes, p.MaxFPS, p.MaxBitrateKbps, boolToInt(p.CustomTURNEnabled), p.CustomTURNURL,
		p.CustomTURNSecretEncrypted)
	if err != nil {
		return fmt.Errorf("setting host preferences: %w", err)
	}
	return nil
}

func scanHostPreferences(sc interface{ Scan(...any) error }) (*HostPreferences, error) {
	var p HostPreferences
	var customTURNEnabled int64
	if err := sc.Scan(&p.HostID, &p.Timezone, &p.YouTubeChannel, &p.TwitchChannel,
		&p.DefaultChannelPlatform, &p.MaxRes, &p.MaxFPS, &p.MaxBitrateKbps, &customTURNEnabled,
		&p.CustomTURNURL, &p.CustomTURNSecretEncrypted); err != nil {
		return nil, err
	}
	p.CustomTURNEnabled = customTURNEnabled != 0
	return &p, nil
}
