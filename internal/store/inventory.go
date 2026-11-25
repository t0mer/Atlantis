package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/t0mer/atlantis/internal/deco"
)

// Node is a stored mesh node.
type Node struct {
	ID, MAC, Nickname, Model, HardwareVer, SoftwareVer, IP, ParentID, Backhaul string
	Master, Online, InternetOnline                                             bool
	FirstSeen, LastSeen                                                        time.Time
}

// SaveNodes upserts the polled nodes. Nodes missing from the poll keep
// their row with online=0, so a dropped satellite stays visible.
func (s *Store) SaveNodes(ctx context.Context, networkID int64, nodes []deco.Device) error {
	ts := s.now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE nodes SET online=0 WHERE network_id=?`, networkID); err != nil {
		return err
	}
	for _, n := range nodes {
		_, err := tx.ExecContext(ctx, `INSERT INTO nodes (network_id, id, mac, nickname, model, hw_ver, fw_ver, master, ip, parent_id, backhaul, online, internet_online, first_seen, last_seen)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT (network_id, id) DO UPDATE SET mac=excluded.mac, nickname=excluded.nickname, model=excluded.model,
			  hw_ver=excluded.hw_ver, fw_ver=excluded.fw_ver, master=excluded.master, ip=excluded.ip, parent_id=excluded.parent_id,
			  backhaul=excluded.backhaul, online=excluded.online, internet_online=excluded.internet_online,
			  last_seen=CASE WHEN excluded.online=1 THEN excluded.last_seen ELSE nodes.last_seen END`,
			networkID, n.ID, n.MAC, n.Nickname, n.Model, n.HardwareVer, n.SoftwareVer, b2i(n.Master), n.IP, n.ParentID,
			n.Backhaul.Type, b2i(n.Online), b2i(n.InternetOnline), ts, ts)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Nodes lists the site's nodes, master first.
func (s *Store) Nodes(ctx context.Context, networkID int64) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, mac, nickname, model, hw_ver, fw_ver, ip, parent_id, backhaul, master, online, internet_online, first_seen, last_seen
		FROM nodes WHERE network_id=? ORDER BY master DESC, nickname`, networkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var n Node
		var master, online, inet int
		var first, last int64
		if err := rows.Scan(&n.ID, &n.MAC, &n.Nickname, &n.Model, &n.HardwareVer, &n.SoftwareVer, &n.IP, &n.ParentID, &n.Backhaul,
			&master, &online, &inet, &first, &last); err != nil {
			return nil, err
		}
		n.Master, n.Online, n.InternetOnline = master == 1, online == 1, inet == 1
		n.FirstSeen, n.LastSeen = time.Unix(first, 0), time.Unix(last, 0)
		out = append(out, n)
	}
	return out, rows.Err()
}

// Client is a stored client with the user's metadata.
type Client struct {
	MAC, Name, Alias, Type, LastNode, LastBand, LastNetwork, LastIP string
	Watch, Online, Wired                                            bool
	Tags                                                            []string
	FirstSeen, LastSeen                                             time.Time
}

// DisplayName is the alias when set, else the device-reported name.
func (c Client) DisplayName() string {
	if c.Alias != "" {
		return c.Alias
	}
	if c.Name != "" {
		return c.Name
	}
	return c.MAC
}

// KnownClients returns the MACs ever seen on the site.
func (s *Store) KnownClients(ctx context.Context, networkID int64) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT mac FROM clients WHERE network_id=?`, networkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out[m] = true
	}
	return out, rows.Err()
}

// SaveClients upserts polled clients. online is the debounced presence
// decided by the events engine (it may differ from the raw poll).
func (s *Store) SaveClients(ctx context.Context, networkID int64, clients []deco.WifiClient, online map[string]bool) error {
	ts := s.now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range clients {
		on := online[c.MAC]
		_, err := tx.ExecContext(ctx, `INSERT INTO clients (network_id, mac, name, type, online, last_node, last_band, last_network, last_ip, wired, first_seen, last_seen)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT (network_id, mac) DO UPDATE SET
			  name=CASE WHEN excluded.name<>'' THEN excluded.name ELSE clients.name END,
			  type=CASE WHEN excluded.type<>'' THEN excluded.type ELSE clients.type END,
			  online=excluded.online,
			  last_node=CASE WHEN excluded.online=1 THEN excluded.last_node ELSE clients.last_node END,
			  last_band=CASE WHEN excluded.online=1 THEN excluded.last_band ELSE clients.last_band END,
			  last_network=CASE WHEN excluded.online=1 THEN excluded.last_network ELSE clients.last_network END,
			  last_ip=CASE WHEN excluded.last_ip<>'' THEN excluded.last_ip ELSE clients.last_ip END,
			  wired=excluded.wired,
			  last_seen=CASE WHEN excluded.online=1 THEN excluded.last_seen ELSE clients.last_seen END`,
			networkID, c.MAC, c.Name, c.Type, b2i(on), c.NodeID, c.Band, c.Network, c.IP, b2i(c.Wired), ts, ts)
		if err != nil {
			return err
		}
	}
	for mac, on := range online { // debounced presence for clients absent from this poll
		if _, err := tx.ExecContext(ctx, `UPDATE clients SET online=? WHERE network_id=? AND mac=?`, b2i(on), networkID, mac); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const clientCols = `mac, name, alias, type, watch, tags, online, last_node, last_band, last_network, last_ip, wired, first_seen, last_seen`

func scanClient(sc interface{ Scan(...any) error }) (Client, error) {
	var c Client
	var watch, online, wired int
	var tags string
	var first, last int64
	err := sc.Scan(&c.MAC, &c.Name, &c.Alias, &c.Type, &watch, &tags, &online, &c.LastNode, &c.LastBand, &c.LastNetwork, &c.LastIP, &wired, &first, &last)
	c.Watch, c.Online, c.Wired = watch == 1, online == 1, wired == 1
	_ = json.Unmarshal([]byte(tags), &c.Tags)
	c.FirstSeen, c.LastSeen = time.Unix(first, 0), time.Unix(last, 0)
	return c, err
}

// Clients lists the site's clients, online first.
func (s *Store) Clients(ctx context.Context, networkID int64) ([]Client, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+clientCols+` FROM clients WHERE network_id=? ORDER BY online DESC, alias, name, mac`, networkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Client
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ErrNotFound is returned for unknown rows.
var ErrNotFound = sql.ErrNoRows

// GetClient returns one client.
func (s *Store) GetClient(ctx context.Context, networkID int64, mac string) (Client, error) {
	return scanClient(s.db.QueryRowContext(ctx, `SELECT `+clientCols+` FROM clients WHERE network_id=? AND mac=?`, networkID, mac))
}

// ClientMeta is the user-editable part of a client.
type ClientMeta struct {
	Alias *string
	Watch *bool
	Tags  []string // nil = unchanged
}

// UpdateClientMeta changes alias, watch flag and tags.
func (s *Store) UpdateClientMeta(ctx context.Context, networkID int64, mac string, m ClientMeta) (Client, error) {
	c, err := s.GetClient(ctx, networkID, mac)
	if err != nil {
		return Client{}, err
	}
	if m.Alias != nil {
		c.Alias = *m.Alias
	}
	if m.Watch != nil {
		c.Watch = *m.Watch
	}
	if m.Tags != nil {
		c.Tags = m.Tags
	}
	tags, _ := json.Marshal(c.Tags)
	if c.Tags == nil {
		tags = []byte("[]")
	}
	_, err = s.db.ExecContext(ctx, `UPDATE clients SET alias=?, watch=?, tags=? WHERE network_id=? AND mac=?`, c.Alias, b2i(c.Watch), string(tags), networkID, mac)
	return c, err
}

// NodeSample is one node observation.
type NodeSample struct {
	NodeID                 string
	Online, InternetOnline bool
	Clients, Signal        int
	CPU, Mem               *float64
}

// ClientSample is one client observation.
type ClientSample struct {
	MAC, NodeID, Band, Network, IP string
	Online                         bool
	UpBps, DownBps                 int64
}

// AddSamples records one poll's observations.
func (s *Store) AddSamples(ctx context.Context, networkID int64, nodes []NodeSample, clients []ClientSample) error {
	ts := s.now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, n := range nodes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO node_samples (network_id, node_id, ts, online, internet_online, clients, signal, cpu, mem) VALUES (?,?,?,?,?,?,?,?,?)`,
			networkID, n.NodeID, ts, b2i(n.Online), b2i(n.InternetOnline), n.Clients, n.Signal, n.CPU, n.Mem); err != nil {
			return err
		}
	}
	for _, c := range clients {
		if _, err := tx.ExecContext(ctx, `INSERT INTO client_samples (network_id, mac, ts, online, node_id, band, network, ip, up_bps, down_bps) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			networkID, c.MAC, ts, b2i(c.Online), c.NodeID, c.Band, c.Network, c.IP, c.UpBps, c.DownBps); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Point is one history point: a raw sample or an hourly rollup.
type Point struct {
	TS             time.Time `json:"ts"`
	Online         float64   `json:"online"` // 0..1 (fraction of the hour for rollups)
	UpBps          int64     `json:"up_bps,omitempty"`
	DownBps        int64     `json:"down_bps,omitempty"`
	NodeID         string    `json:"node_id,omitempty"`
	Band           string    `json:"band,omitempty"`
	Clients        float64   `json:"clients,omitempty"`
	CPU            *float64  `json:"cpu,omitempty"`
	Mem            *float64  `json:"mem,omitempty"`
	InternetOnline *bool     `json:"internet_online,omitempty"`
}

// ClientHistory returns raw samples since t, preceded by hourly rollups
// for hours that have no raw samples left.
func (s *Store) ClientHistory(ctx context.Context, networkID int64, mac string, since time.Time) ([]Point, error) {
	var out []Point
	rows, err := s.db.QueryContext(ctx, `SELECT hour, samples, online_count, avg_up_bps, avg_down_bps FROM client_rollups
		WHERE network_id=? AND mac=? AND hour>=? AND hour < COALESCE((SELECT MIN(ts) FROM client_samples WHERE network_id=? AND mac=?), 9223372036854775807) ORDER BY hour`,
		networkID, mac, since.Unix(), networkID, mac)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var h, n, on, up, down int64
		if err := rows.Scan(&h, &n, &on, &up, &down); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, Point{TS: time.Unix(h, 0), Online: float64(on) / float64(max(n, 1)), UpBps: up, DownBps: down})
	}
	rows.Close()
	rows, err = s.db.QueryContext(ctx, `SELECT ts, online, up_bps, down_bps, node_id, band FROM client_samples WHERE network_id=? AND mac=? AND ts>=? ORDER BY ts`,
		networkID, mac, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ts, on, up, down int64
		var p Point
		if err := rows.Scan(&ts, &on, &up, &down, &p.NodeID, &p.Band); err != nil {
			return nil, err
		}
		p.TS, p.Online, p.UpBps, p.DownBps = time.Unix(ts, 0), float64(on), up, down
		out = append(out, p)
	}
	return out, rows.Err()
}

// NodeHistory is ClientHistory for a node.
func (s *Store) NodeHistory(ctx context.Context, networkID int64, nodeID string, since time.Time) ([]Point, error) {
	var out []Point
	rows, err := s.db.QueryContext(ctx, `SELECT hour, samples, online_count, avg_clients, avg_cpu, avg_mem FROM node_rollups
		WHERE network_id=? AND node_id=? AND hour>=? AND hour < COALESCE((SELECT MIN(ts) FROM node_samples WHERE network_id=? AND node_id=?), 9223372036854775807) ORDER BY hour`,
		networkID, nodeID, since.Unix(), networkID, nodeID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var h, n, on int64
		var p Point
		if err := rows.Scan(&h, &n, &on, &p.Clients, &p.CPU, &p.Mem); err != nil {
			rows.Close()
			return nil, err
		}
		p.TS, p.Online = time.Unix(h, 0), float64(on)/float64(max(n, 1))
		out = append(out, p)
	}
	rows.Close()
	rows, err = s.db.QueryContext(ctx, `SELECT ts, online, internet_online, clients, cpu, mem FROM node_samples WHERE network_id=? AND node_id=? AND ts>=? ORDER BY ts`,
		networkID, nodeID, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ts, on, inet, cl int64
		var p Point
		if err := rows.Scan(&ts, &on, &inet, &cl, &p.CPU, &p.Mem); err != nil {
			return nil, err
		}
		in := inet == 1
		p.TS, p.Online, p.Clients, p.InternetOnline = time.Unix(ts, 0), float64(on), float64(cl), &in
		out = append(out, p)
	}
	return out, rows.Err()
}
