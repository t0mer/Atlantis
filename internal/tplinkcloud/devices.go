package tplinkcloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// DecoDeviceType is the cloud device type of Deco units.
const DecoDeviceType = "HOMEWIFISYSTEM"

const (
	pageSize     = 300 // k1.java f60871x
	featureBatch = 100 // k1.java f60873z
	maxDevices   = 3000
)

// Site is one Deco mesh network on the account, addressed through its main
// (master) unit.
type Site struct {
	ID             string // group_id; the master's device ID when unknown
	Name           string
	MasterDeviceID string
	AppServerURL   string // relay host for this site
	Online         bool
	Owner          bool // the account owns the site (vs. shared with it)
	OperationMode  string
	// AccessPlatform is the cloud platform the master is on. The app's ATA
	// relay is only used for UNKNOWN/BASIC (mesh/context/c.java F9()).
	AccessPlatform string
	Nodes          []Node
}

// Node is one Deco unit of a site.
type Node struct {
	DeviceID string // empty for satellites the cloud lists without an ID
	MAC      string // aa:bb:cc:dd:ee:ff
	Model    string
	Name     string
	Master   bool
}

type cloudDevice struct {
	DeviceID       string `json:"deviceId"`
	DeviceType     string `json:"deviceType"`
	DeviceMAC      string `json:"deviceMac"`
	DeviceModel    string `json:"deviceModel"`
	DeviceName     string `json:"deviceName"`
	Alias          string `json:"alias"`
	FwVer          string `json:"fwVer"`
	AppServerURL   string `json:"appServerUrl"`
	AppServerURLV2 string `json:"appServerUrlV2"`
	Role           int    `json:"role"`
	Status         int    `json:"status"`
	AccessPlatform string `json:"accessPlatform"`
}

func (d cloudDevice) serverURL() string {
	if d.AppServerURLV2 != "" {
		return d.AppServerURLV2
	}
	return d.AppServerURL
}

type deviceListPage struct {
	CurrentIndex int           `json:"currentIndex"`
	TotalNum     int           `json:"totalNum"`
	DeviceList   []cloudDevice `json:"deviceList"`
}

type featureData struct {
	GroupID        string `json:"group_id"`
	GroupName      string `json:"group_name"`
	MasterDeviceID string `json:"master_device_id"`
	Role           string `json:"role"`
	ReList         []struct {
		DeviceID       string `json:"device_id"`
		MAC            string `json:"mac"`
		DeviceModel    string `json:"device_model"`
		Nickname       string `json:"nickname"`
		CustomNickname string `json:"custom_nickname"`
	} `json:"re_list"`
}

type feature struct {
	Data    featureData `json:"data"`
	TMPData struct {
		OperationMode string `json:"operation_mode"`
	} `json:"tmp_data"`
}

// devices lists every Deco unit bound to the account.
func (c *Client) devices(ctx context.Context) ([]cloudDevice, error) {
	var all []cloudDevice
	for index := 0; ; {
		var page deviceListPage
		err := c.call(ctx, c.accountServerURL(), "/api/v2/common/getDeviceListByPage", map[string]any{
			"index":          index,
			"limit":          pageSize,
			"deviceTypeList": []string{DecoDeviceType},
		}, &page)
		if err != nil {
			return nil, fmt.Errorf("listing devices: %w", err)
		}
		all = append(all, page.DeviceList...)
		index += len(page.DeviceList)
		if len(page.DeviceList) == 0 || index >= page.TotalNum || len(all) >= maxDevices {
			return all, nil
		}
	}
}

// features fetches mesh data (role, group, satellites) per device ID.
func (c *Client) features(ctx context.Context, ids []string) (map[string]feature, error) {
	out := make(map[string]feature, len(ids))
	for start := 0; start < len(ids); start += featureBatch {
		batch := ids[start:min(start+featureBatch, len(ids))]
		var res struct {
			FeatureInfo map[string]json.RawMessage `json:"featureInfo"`
		}
		err := c.call(ctx, c.accountServerURL(), "/api/v2/common/getDeviceFeatureInfo", map[string]any{
			"cloudUserName": c.Email(),
			"deviceIdList":  batch,
		}, &res)
		if err != nil {
			return nil, fmt.Errorf("reading mesh features: %w", err)
		}
		for id, raw := range res.FeatureInfo {
			var f feature
			// featureInfo values may be JSON objects or JSON-encoded strings.
			var s string
			if json.Unmarshal(raw, &s) == nil {
				raw = json.RawMessage(s)
			}
			if json.Unmarshal(raw, &f) == nil {
				out[id] = f
			}
		}
	}
	return out, nil
}

func (c *Client) accountServerURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.accountURL
}

// Sites lists the Deco networks on the account, one per main unit.
func (c *Client) Sites(ctx context.Context) ([]Site, error) {
	devs, err := c.devices(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(devs))
	for _, d := range devs {
		ids = append(ids, d.DeviceID)
	}
	feats, err := c.features(ctx, ids)
	if err != nil {
		return nil, err
	}
	return buildSites(devs, feats), nil
}

// buildSites groups devices into sites: every unit whose feature role is
// "master" (or that has no feature data) roots a site, and the satellites in
// its re_list become its nodes (l1.java B0()).
func buildSites(devs []cloudDevice, feats map[string]feature) []Site {
	satellite := map[string]bool{}
	for _, f := range feats {
		for _, re := range f.Data.ReList {
			satellite[re.DeviceID] = true
		}
	}
	var sites []Site
	for _, d := range devs {
		f, ok := feats[d.DeviceID]
		if ok && f.Data.Role != "" && !strings.EqualFold(f.Data.Role, "master") {
			continue // satellites are listed under their master
		}
		if satellite[d.DeviceID] && !strings.EqualFold(f.Data.Role, "master") {
			continue // a satellite without its own mesh data
		}
		s := Site{
			ID:             f.Data.GroupID,
			Name:           decodeB64(f.Data.GroupName),
			MasterDeviceID: d.DeviceID,
			AppServerURL:   d.serverURL(),
			Online:         d.Status == 1,
			Owner:          d.Role == 0,
			OperationMode:  f.TMPData.OperationMode,
			AccessPlatform: d.AccessPlatform,
			Nodes: []Node{{
				DeviceID: d.DeviceID, MAC: normalizeMAC(d.DeviceMAC), Model: d.DeviceModel,
				Name: firstNonEmpty(decodeB64(d.Alias), decodeB64(d.DeviceName)), Master: true,
			}},
		}
		if s.ID == "" {
			s.ID = d.DeviceID
		}
		if s.Name == "" {
			s.Name = s.Nodes[0].Name
		}
		for _, re := range f.Data.ReList {
			s.Nodes = append(s.Nodes, Node{
				DeviceID: re.DeviceID, MAC: normalizeMAC(re.MAC), Model: re.DeviceModel,
				Name: firstNonEmpty(decodeB64(re.CustomNickname), decodeB64(re.Nickname)),
			})
		}
		sites = append(sites, s)
	}
	sort.SliceStable(sites, func(i, j int) bool { return sites[i].Name < sites[j].Name })
	return sites
}

// decodeB64 decodes base64 UTF-8 text, returning s unchanged if it is not.
func decodeB64(s string) string {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && utf8.Valid(b) && len(b) > 0 {
		return string(b)
	}
	return s
}

// normalizeMAC turns AABBCCDDEEFF or AA-BB-CC-DD-EE-FF into aa:bb:cc:dd:ee:ff.
func normalizeMAC(s string) string {
	h := strings.ToLower(strings.NewReplacer("-", "", ":", "").Replace(s))
	if len(h) != 12 {
		return strings.ToLower(s)
	}
	var b strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(h[i : i+2])
	}
	return b.String()
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
