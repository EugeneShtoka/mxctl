package matrix

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/zalando/go-keyring"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"mxctl/internal/hooks"
)

type Severity string

const (
	SeverityLow      Severity = "low"
	SeverityNormal   Severity = "normal"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

type AliasConfig struct {
	Name     string   `toml:"name"`
	Severity Severity `toml:"severity,omitempty"`
	Color    string   `toml:"color,omitempty"`
}

const keyringService = "mxctl"

type Config struct {
	Homeserver  string                 `toml:"homeserver"`
	UserID      string                 `toml:"user_id"`
	AccessToken string                 `toml:"-"`
	DeviceID    string                 `toml:"device_id"`
	SelfIDs      []string               `toml:"self_ids,omitempty"`
	SpammerIDs   []string               `toml:"spammer_ids,omitempty"`
	Plugins      []hooks.PluginConfig   `toml:"plugins,omitempty"`
	Aliases      map[string]AliasConfig `toml:"aliases,omitempty"`
	RoomAliases  map[string]AliasConfig `toml:"room_aliases,omitempty"`
	DisableDedup bool                   `toml:"disable_dedup,omitempty"`
}

const dedupBodyWindow = 2 * time.Second

type Client struct {
	mx           *mautrix.Client
	cfg          *Config
	displayNames map[string]string    // sender Matrix ID → display name cache
	roomNames    map[id.RoomID]string // room ID → display name cache
	seenBodies   map[uint64]time.Time // body hash → first seen time (bridge dedup)
}

func New(cfg *Config) (*Client, error) {
	mx, err := mautrix.NewClient(cfg.Homeserver, id.UserID(cfg.UserID), cfg.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}
	return &Client{
		mx:           mx,
		cfg:          cfg,
		displayNames: make(map[string]string),
		roomNames:    make(map[id.RoomID]string),
		seenBodies:   make(map[uint64]time.Time),
	}, nil
}

func Login(homeserver, userID, password string) (*Config, error) {
	mx, err := mautrix.NewClient(homeserver, "", "")
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}

	resp, err := mx.Login(context.Background(), &mautrix.ReqLogin{
		Type:                     mautrix.AuthTypePassword,
		Identifier:               mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: userID},
		Password:                 password,
		InitialDeviceDisplayName: "mxctl",
		StoreCredentials:         false,
	})
	if err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}

	return &Config{
		Homeserver:  homeserver,
		UserID:      userID,
		AccessToken: resp.AccessToken,
		DeviceID:    string(resp.DeviceID),
	}, nil
}

func (c *Client) Sync(ctx context.Context) error {
	// Get current position token without processing any history.
	resp, err := c.mx.SyncRequest(ctx, 30000, "", "", true, event.PresenceOffline)
	if err != nil {
		return fmt.Errorf("initial sync: %w", err)
	}
	since := resp.NextBatch
	fmt.Fprintln(os.Stderr, "Ready. Listening for new messages...")

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		resp, err := c.mx.SyncRequest(ctx, 30000, since, "", false, event.PresenceOffline)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintf(os.Stderr, "sync error: %v — retrying in 5s\n", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
			}
			continue
		}

		// Seed display name cache from state events in this sync chunk.
		for _, roomData := range resp.Rooms.Join {
			for sender, name := range extractDisplayNames(*roomData) {
				c.displayNames[sender] = name
			}
		}

		for roomID, roomData := range resp.Rooms.Join {
			roomAlias := c.cfg.RoomAliases[string(roomID)]
			roomName := roomAlias.Name
			if roomName == "" {
				roomName = c.resolveRoomName(ctx, roomID, *roomData)
			}
			for _, evt := range roomData.Timeline.Events {
				if evt.Type != event.EventMessage {
					continue
				}

				body, msgType := extractBody(evt)
				if body == "" {
					continue
				}

				// Deduplicate by body hash within a 2-second window to suppress
				// the same message arriving through multiple bridges simultaneously.
				if !c.cfg.DisableDedup {
					now := time.Now()
					bodyHash := hashString(body)
					if t, ok := c.seenBodies[bodyHash]; ok && now.Sub(t) < dedupBodyWindow {
						continue
					}
					c.seenBodies[bodyHash] = now
				}

				senderAlias := c.cfg.Aliases[string(evt.Sender)]
				senderName := senderAlias.Name
				if senderName == "" {
					senderName = c.resolveSenderName(ctx, string(evt.Sender))
				}

				severity := maxSeverity(senderAlias.Severity, roomAlias.Severity)
				color := senderAlias.Color
				if color == "" {
					color = roomAlias.Color
				}

				fmt.Fprintf(os.Stderr, "event %s from %s (%s) in %s\n", evt.ID, evt.Sender, senderName, roomName)
				hooks.Send(c.cfg.Plugins, &hooks.Event{
					EventID:    string(evt.ID),
					RoomID:     string(roomID),
					RoomName:   roomName,
					Sender:     string(evt.Sender),
					SenderName: senderName,
					Body:       body,
					MsgType:    msgType,
					TS:         evt.Timestamp,
					Severity:   string(severity),
					Color:      color,
				}, c.cfg.SelfIDs, c.cfg.SpammerIDs)
			}
		}

		// Sweep expired body dedup entries once per sync cycle.
		if !c.cfg.DisableDedup {
			now := time.Now()
			for h, t := range c.seenBodies {
				if now.Sub(t) >= dedupBodyWindow {
					delete(c.seenBodies, h)
				}
			}
		}

		since = resp.NextBatch
	}
}

// resolveSenderName returns the display name for a Matrix user ID, fetching
// from the profile API on first encounter and caching the result.
func (c *Client) resolveSenderName(ctx context.Context, senderID string) string {
	if name, ok := c.displayNames[senderID]; ok {
		return name
	}
	profile, err := c.mx.GetProfile(ctx, id.UserID(senderID))
	if err != nil {
		fmt.Fprintf(os.Stderr, "profile fetch %s: %v\n", senderID, err)
		c.displayNames[senderID] = "" // cache the miss to avoid repeated failed fetches
		return ""
	}
	c.displayNames[senderID] = profile.DisplayName
	return profile.DisplayName
}

func extractDisplayNames(data mautrix.SyncJoinedRoom) map[string]string {
	names := make(map[string]string)
	for _, evt := range data.State.Events {
		if evt.Type != event.StateMember {
			continue
		}
		if name, ok := evt.Content.Raw["displayname"].(string); ok && name != "" {
			names[string(evt.GetStateKey())] = name
		}
	}
	return names
}

func (c *Client) resolveRoomName(ctx context.Context, roomID id.RoomID, data mautrix.SyncJoinedRoom) string {
	// Seed from state events in this sync chunk.
	for _, evt := range data.State.Events {
		if evt.Type == event.StateRoomName {
			if name, ok := evt.Content.Raw["name"].(string); ok && name != "" {
				c.roomNames[roomID] = name
				return name
			}
		}
	}
	// Return cached name if we already fetched it.
	if name, ok := c.roomNames[roomID]; ok {
		return name
	}
	// Fetch from API and cache.
	var content struct {
		Name string `json:"name"`
	}
	if err := c.mx.StateEvent(ctx, roomID, event.StateRoomName, "", &content); err != nil {
		fmt.Fprintf(os.Stderr, "room name fetch %s: %v\n", roomID, err)
		c.roomNames[roomID] = "" // cache miss to avoid repeated failed fetches
	} else {
		c.roomNames[roomID] = content.Name
	}
	return c.roomNames[roomID]
}

func hashString(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

func extractBody(evt *event.Event) (body, msgType string) {
	content, ok := evt.Content.Raw["body"].(string)
	if !ok {
		return "", ""
	}
	mt, _ := evt.Content.Raw["msgtype"].(string)
	return content, mt
}

func severityRank(s Severity) int {
	switch s {
	case SeverityLow:
		return 1
	case SeverityNormal:
		return 2
	case SeverityHigh:
		return 3
	case SeverityCritical:
		return 4
	default:
		return 0
	}
}

func maxSeverity(a, b Severity) Severity {
	if severityRank(a) >= severityRank(b) {
		return a
	}
	return b
}

func SaveConfig(path string, cfg *Config) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(cfg); err != nil {
		return err
	}
	return keyring.Set(keyringService, cfg.UserID, cfg.AccessToken)
}

func LoadConfig(path string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, err
	}
	token, err := keyring.Get(keyringService, cfg.UserID)
	if err != nil {
		return nil, fmt.Errorf("get access token from keyring: %w", err)
	}
	cfg.AccessToken = token
	return &cfg, nil
}
