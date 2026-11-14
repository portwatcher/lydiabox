package cloudflare

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Zone is a DNS zone of the account.
type Zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// FindZone returns the zone that holds hostname. With an explicit zone name
// it looks up only that zone; otherwise it tries the longest parent domain
// first (agent.example.com, then example.com).
func (c *Client) FindZone(ctx context.Context, hostname, explicit string) (Zone, error) {
	candidates := []string{strings.ToLower(explicit)}
	if explicit == "" {
		candidates = parentDomains(hostname)
	} else if hostname != candidates[0] && !strings.HasSuffix(hostname, "."+candidates[0]) {
		return Zone{}, fmt.Errorf("cloudflare: %s is not inside zone %s", hostname, explicit)
	}
	for _, name := range candidates {
		q := url.Values{"name": {name}, "account.id": {c.accountID}}
		zones, err := list[Zone](ctx, c, "/zones", q)
		if err != nil {
			return Zone{}, err
		}
		for _, z := range zones {
			if strings.EqualFold(z.Name, name) {
				return z, nil
			}
		}
	}
	return Zone{}, fmt.Errorf("cloudflare: no zone in account %s holds %s; add the domain to Cloudflare or pass -zone", c.accountID, hostname)
}

// parentDomains lists hostname and its parents with at least two labels.
func parentDomains(hostname string) []string {
	labels := strings.Split(strings.ToLower(hostname), ".")
	var out []string
	for i := 0; i+2 <= len(labels); i++ {
		out = append(out, strings.Join(labels[i:], "."))
	}
	return out
}

// DNSRecord is one DNS record.
type DNSRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl,omitempty"`
	Comment string `json:"comment,omitempty"`
}

// DNSRecords returns every record called name in the zone.
func (c *Client) DNSRecords(ctx context.Context, zoneID, name string) ([]DNSRecord, error) {
	return list[DNSRecord](ctx, c, "/zones/"+esc(zoneID)+"/dns_records", url.Values{"name": {name}})
}

// CreateDNSRecord adds a record to the zone.
func (c *Client) CreateDNSRecord(ctx context.Context, zoneID string, rec DNSRecord) (DNSRecord, error) {
	var out DNSRecord
	_, err := c.do(ctx, http.MethodPost, "/zones/"+esc(zoneID)+"/dns_records", nil, rec, &out)
	return out, err
}

// DeleteDNSRecord removes a record.
func (c *Client) DeleteDNSRecord(ctx context.Context, zoneID, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/zones/"+esc(zoneID)+"/dns_records/"+esc(id), nil, nil, nil)
	return err
}
