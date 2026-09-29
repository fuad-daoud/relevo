package serve

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
)

var (
	ErrAlreadyEnrolled = errors.New("client already enrolled")
	ErrNoSuchClient    = errors.New("no such client")
)

// clientsKVKey is the kv row holding the enrolled clients.
const clientsKVKey = "serve.clients"

type Client struct {
	ID         remote.ClientID `json:"id"`
	Label      string          `json:"label"`
	PubKey     string          `json:"pubkey"` // the enrollment line, remote.MarshalPublic form
	EnrolledAt time.Time       `json:"enrolled_at"`
	RevokedAt  time.Time       `json:"revoked_at,omitempty"`
}

// Clients is the enrolled-client list, read from the serve.clients kv row.
// Every public method re-reads the row, so another process's enroll or revoke
// is seen live.
type Clients struct {
	kv         db.KV
	mu         sync.Mutex
	list       []Client
	warnLogged map[string]bool
}

// refresh reads the whole document from the kv row. An absent row is an empty
// list.
func (c *Clients) refresh() error {
	if c.kv == nil {
		return nil
	}
	data, ok, err := c.kv.KVGet(clientsKVKey)
	if err != nil {
		return err
	}
	if !ok {
		c.list = nil
		return nil
	}
	var parsed []Client
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	c.list = parsed
	return nil
}

func (c *Clients) warnOnceLocked(err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	if c.warnLogged == nil {
		c.warnLogged = make(map[string]bool)
	}
	if !c.warnLogged[msg] {
		c.warnLogged[msg] = true
		slog.Warn("refresh clients", "key", clientsKVKey, "err", err)
	}
}

// LoadClients returns the client list held in kv's serve.clients row.
func LoadClients(kv db.KV) (*Clients, error) {
	c := &Clients{kv: kv}
	if err := c.refresh(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Clients) Lookup(id remote.ClientID) (ed25519.PublicKey, remote.KeyStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.refresh(); err != nil {
		c.warnOnceLocked(err)
	}

	for _, cl := range c.list {
		if cl.ID == id {
			if !cl.RevokedAt.IsZero() {
				return nil, remote.KeyRevoked
			}
			pub, err := remote.ParsePublic(cl.PubKey)
			if err != nil {
				return nil, remote.KeyUnknown
			}
			return pub, remote.KeyActive
		}
	}
	return nil, remote.KeyUnknown
}

func (c *Clients) Add(label, pubLine string, now time.Time) (Client, error) {
	pub, err := remote.ParsePublic(pubLine)
	if err != nil {
		return Client{}, err
	}
	id := remote.IDOf(pub)

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.refresh(); err != nil {
		return Client{}, err
	}

	for i, cl := range c.list {
		if cl.ID == id {
			if cl.RevokedAt.IsZero() {
				return Client{}, ErrAlreadyEnrolled
			}
			// re-enrolling a revoked id clears RevokedAt
			c.list[i].RevokedAt = time.Time{}
			if label != "" {
				c.list[i].Label = label
			}
			c.list[i].PubKey = pubLine
			if err := c.saveLocked(); err != nil {
				return Client{}, err
			}
			return c.list[i], nil
		}
	}

	cl := Client{
		ID:         id,
		Label:      label,
		PubKey:     pubLine,
		EnrolledAt: now,
	}
	c.list = append(c.list, cl)
	if err := c.saveLocked(); err != nil {
		return Client{}, err
	}
	return cl, nil
}

func (c *Clients) Revoke(id remote.ClientID, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.refresh(); err != nil {
		return err
	}

	for i, cl := range c.list {
		if cl.ID == id {
			c.list[i].RevokedAt = now
			return c.saveLocked()
		}
	}
	return ErrNoSuchClient
}

func (c *Clients) List() []Client {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.refresh(); err != nil {
		c.warnOnceLocked(err)
	}

	out := make([]Client, len(c.list))
	copy(out, c.list)
	return out
}

func (c *Clients) LabelOf(id remote.ClientID) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.refresh(); err != nil {
		c.warnOnceLocked(err)
	}

	for _, cl := range c.list {
		if cl.ID == id && cl.Label != "" {
			return cl.Label
		}
	}
	s := string(id)
	s = strings.TrimPrefix(s, "SHA256:")
	if len(s) > 8 {
		s = s[:8]
	}
	return s
}

// saveLocked writes the whole document to the kv row.
func (c *Clients) saveLocked() error {
	if c.kv == nil {
		return nil
	}
	data, err := json.MarshalIndent(c.list, "", "  ")
	if err != nil {
		return err
	}
	return c.kv.KVPut(clientsKVKey, data)
}
