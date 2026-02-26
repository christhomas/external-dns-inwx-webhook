package inwx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	inwx "github.com/nrdcg/goinwx"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
	"sigs.k8s.io/external-dns/provider"
)

type INWXProvider struct {
	provider.BaseProvider
	client       AbstractClientWrapper
	domainFilter *endpoint.DomainFilter
	logger       *slog.Logger
}

func NewINWXProvider(domainFilter *[]string, username string, password string, sandbox bool, logger *slog.Logger) *INWXProvider {
	p := &INWXProvider{
		client:       &ClientWrapper{client: inwx.NewClient(username, password, &inwx.ClientOptions{Sandbox: sandbox})},
		domainFilter: endpoint.NewDomainFilter(*domainFilter),
		logger:       logger,
	}

	if _, err := p.client.login(); err != nil {
		logger.Error("startup zone check: failed to login", "err", err)
	} else {
		if zones, err := p.client.getZones(); err != nil {
			logger.Error("startup zone check: failed to list zones", "err", err)
		} else {
			logger.Info("INWX zones available", "count", len(*zones), "zones", strings.Join(*zones, ", "))
		}
		if err := p.client.logout(); err != nil {
			logger.Error("startup zone check: failed to logout", "err", err)
		}
	}

	return p
}

func (p *INWXProvider) Records(ctx context.Context) ([]*endpoint.Endpoint, error) {
	endpoints := make([]*endpoint.Endpoint, 0)

	if _, err := p.client.login(); err != nil {
		return nil, err
	}
	defer func() {
		if err := p.client.logout(); err != nil {
			slog.Error("error encountered while logging out", "err", err)
		}
	}()

	zones, err := p.client.getZones()
	if err != nil {
		return nil, err
	}

	for _, zone := range *zones {
		records, err := p.client.getRecords(zone)
		if err != nil {
			return nil, fmt.Errorf("unable to query DNS zone info for zone '%v': %v", zone, err)
		}
		for _, rec := range *records {
			name := fmt.Sprintf("%s.%s", rec.Name, zone)
			ep := endpoint.NewEndpointWithTTL(name, rec.Type, endpoint.TTL(rec.TTL), rec.Content)
			endpoints = append(endpoints, ep)
		}
	}
	for _, endpointItem := range endpoints {
		p.logger.Debug("endpoints collected", "endpoints", endpointItem.String())
	}
	return endpoints, nil
}

func (p *INWXProvider) ApplyChanges(ctx context.Context, changes *plan.Changes) error {
	if !changes.HasChanges() {
		p.logger.Debug("no changes detected - nothing to do")
		return nil
	}

	if _, err := p.client.login(); err != nil {
		return err
	}
	defer func() {
		if err := p.client.logout(); err != nil {
			slog.Error("error encountered while logging out", "err", err)
		}
	}()

	zones, err := p.client.getZones()
	if err != nil {
		return err
	}

	errs := []error{}

	recordsCache := map[string]*[]inwx.NameserverRecord{}
	for _, ep := range changes.Delete {
		zone, err := getZone(zones, ep)
		if err != nil {
			errs = append(errs, err)
			slog.Error("failed to find zone for endpoint", "err", err)
		} else {
			if _, ok := recordsCache[zone]; !ok {
				if recs, err := p.client.getRecords(zone); err != nil {
					errs = append(errs, err)
					slog.Error("failed to query DNS zone info", "zone", zone, "err", err)
					continue
				} else {
					recordsCache[zone] = recs
				}
			}
			recIDs, err := getRecIDs(zone, recordsCache[zone], *ep)
			if err != nil {
				errs = append(errs, err)
				slog.Error("failed to look up records to delete", "err", err)
			}
			for _, id := range recIDs {
				if err = p.client.deleteRecord(id); err != nil {
					errs = append(errs, err)
					slog.Error("failed to delete record", "id", id, "ep", ep, "err", err)
				}
			}
		}
	}

	recordsCache = map[string]*[]inwx.NameserverRecord{}
	for _, ep := range changes.Create {
		zone, err := getZone(zones, ep)
		if err != nil {
			errs = append(errs, err)
			slog.Error("failed to find zone for endpoint", "err", err)
			continue
		}
		if _, ok := recordsCache[zone]; !ok {
			if recs, err := p.client.getRecords(zone); err != nil {
				errs = append(errs, err)
				slog.Error("failed to query DNS zone info", "zone", zone, "err", err)
				continue
			} else {
				recordsCache[zone] = recs
			}
		}
		name := extractRecordName(ep.DNSName, zone)
		for _, target := range ep.Targets {
			existing := findRecordsByNameAndType(zone, recordsCache[zone], ep.DNSName, ep.RecordType)

			rec := &inwx.NameserverRecordRequest{
				Domain:  zone,
				Name:    name,
				Type:    ep.RecordType,
				TTL:     int(ep.RecordTTL),
				Content: target,
			}

			// If exact record (same content) already exists, skip
			if findExactRecord(existing, target) != "" {
				slog.Debug("record already exists, skipping create", "name", ep.DNSName, "type", ep.RecordType, "content", target)
				continue
			}

			// If there's exactly one existing record with this name+type and the
			// endpoint has a single target, update instead of creating a duplicate
			if len(existing) == 1 && len(ep.Targets) == 1 {
				slog.Info("record exists with different content, updating instead of creating",
					"name", ep.DNSName, "type", ep.RecordType,
					"old_content", existing[0].Content, "new_content", target)
				if err = p.client.updateRecord(existing[0].ID, rec); err != nil {
					errs = append(errs, err)
					slog.Error("failed to update existing record", "rec", rec, "err", err)
				}
				continue
			}

			if err = p.client.createRecord(rec); err != nil {
				if isObjectExistsError(err) {
					slog.Debug("record already exists in INWX, skipping",
						"name", ep.DNSName, "type", ep.RecordType, "content", target)
				} else {
					errs = append(errs, err)
					slog.Error("failed to create record", "rec", rec, "err", err)
				}
			}
		}
	}

	recordsCache = map[string]*[]inwx.NameserverRecord{}
	for i, oldEp := range changes.UpdateOld {
		newEp := changes.UpdateNew[i]
		zone, err := getZone(zones, oldEp)
		if err != nil {
			errs = append(errs, err)
			slog.Error("failed to update DNS record for endpoint", "err", err)
		} else {
			if _, ok := recordsCache[zone]; !ok {
				if recs, err := p.client.getRecords(zone); err != nil {
					errs = append(errs, err)
					slog.Error("failed to query DNS zone info", "zone", zone, "err", err)
					continue
				} else {
					recordsCache[zone] = recs
				}
			}
			recIDs, err := getRecIDs(zone, recordsCache[zone], *oldEp)
			name := extractRecordName(newEp.DNSName, zone)

			// If old records not found, fall back to upsert for new targets
			if err != nil {
				slog.Warn("old records not found for update, falling back to upsert",
					"endpoint", oldEp.DNSName, "err", err)
				existing := findRecordsByNameAndType(zone, recordsCache[zone], newEp.DNSName, newEp.RecordType)
				for _, target := range newEp.Targets {
					if findExactRecord(existing, target) != "" {
						continue
					}
					rec := &inwx.NameserverRecordRequest{
						Domain:  zone,
						Name:    name,
						Type:    newEp.RecordType,
						TTL:     int(newEp.RecordTTL),
						Content: target,
					}
					if err = p.client.createRecord(rec); err != nil {
						if isObjectExistsError(err) {
							slog.Debug("record already exists in INWX, skipping",
								"name", newEp.DNSName, "type", newEp.RecordType, "content", target)
						} else {
							errs = append(errs, err)
							slog.Error("failed to create record during update fallback", "rec", rec, "err", err)
						}
					}
				}
				continue
			}

			for j := range max(len(oldEp.Targets), len(newEp.Targets), len(recIDs)) {
				switch {
				case j >= len(newEp.Targets):
					if err = p.client.deleteRecord(recIDs[j]); err != nil {
						errs = append(errs, err)
						slog.Error("failed to delete record", "target", oldEp.Targets[j], "ep", oldEp, "err", err)
					}
				case j >= len(oldEp.Targets):
					rec := &inwx.NameserverRecordRequest{
						Domain:  zone,
						Name:    name,
						Type:    newEp.RecordType,
						TTL:     int(newEp.RecordTTL),
						Content: newEp.Targets[j],
					}
					if err = p.client.createRecord(rec); err != nil {
						if isObjectExistsError(err) {
							slog.Debug("record already exists in INWX, skipping",
								"name", newEp.DNSName, "type", newEp.RecordType, "content", newEp.Targets[j])
						} else {
							errs = append(errs, err)
							slog.Error("failed to create record", "rec", rec, "err", err)
						}
					}
				default:
					rec := &inwx.NameserverRecordRequest{
						Domain:  zone,
						Name:    name,
						Type:    newEp.RecordType,
						TTL:     int(oldEp.RecordTTL),
						Content: newEp.Targets[j],
					}
					if err = p.client.updateRecord(recIDs[j], rec); err != nil {
						errs = append(errs, err)
						slog.Error("failed to update record", "rec", rec, "err", err)
					}
				}
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("encountered %d errors while applying changes", len(errs))
	} else {
		return nil
	}
}

// isObjectExistsError returns true if the error is an INWX API error with code 2302 (Object exists).
func isObjectExistsError(err error) bool {
	var apiErr *inwx.ErrorResponse
	if errors.As(err, &apiErr) {
		return apiErr.Code == 2302
	}
	return false
}

// extractRecordName computes the INWX record name from a full DNS name and zone.
// It also strips trailing zone labels that leak into the record name, which happens
// with external-dns's apex domain ownership records (e.g., _edns.a-domain.com.domain.com
// → record name "a-domain.com" → stripped to "a-domain" since INWX rejects names
// that look like domain names).
func extractRecordName(dnsName string, zone string) string {
	if dnsName == zone {
		return ""
	}
	name := strings.TrimSuffix(dnsName, "."+zone)

	// Strip trailing labels that match the zone's labels.
	// e.g., name="_edns.a-beersandbusiness.com", zone="beersandbusiness.com"
	// → labels ["_edns","a-beersandbusiness","com"] vs zone labels ["beersandbusiness","com"]
	// → strip "com" → "_edns.a-beersandbusiness"
	nameLabels := strings.Split(name, ".")
	zoneLabels := strings.Split(zone, ".")
	stripped := 0
	for stripped < len(zoneLabels) && stripped < len(nameLabels)-1 {
		ni := len(nameLabels) - 1 - stripped
		zi := len(zoneLabels) - 1 - stripped
		if nameLabels[ni] == zoneLabels[zi] {
			stripped++
		} else {
			break
		}
	}
	if stripped > 0 {
		name = strings.Join(nameLabels[:len(nameLabels)-stripped], ".")
	}

	return name
}

func getRecIDs(zone string, records *[]inwx.NameserverRecord, ep endpoint.Endpoint) ([]string, error) {
	targetName := extractRecordName(ep.DNSName, zone)
	recIDs := []string{}
	for _, target := range ep.Targets {
		for _, record := range *records {
			if ep.RecordType == record.Type && target == record.Content && record.Name == targetName {
				recIDs = append(recIDs, record.ID)
			}
		}
	}
	if len(recIDs) != len(ep.Targets) {
		return nil, fmt.Errorf("failed to map all endpoint targets to entries")
	}
	return recIDs, nil
}

// findRecordsByNameAndType returns existing records matching the given DNS name and record type.
func findRecordsByNameAndType(zone string, records *[]inwx.NameserverRecord, dnsName string, recordType string) []inwx.NameserverRecord {
	targetName := extractRecordName(dnsName, zone)
	var matches []inwx.NameserverRecord
	for _, record := range *records {
		if recordType == record.Type && record.Name == targetName {
			matches = append(matches, record)
		}
	}
	return matches
}

// findExactRecord returns the ID of a record matching the given content, or empty string if not found.
func findExactRecord(records []inwx.NameserverRecord, content string) string {
	for _, rec := range records {
		if rec.Content == content {
			return rec.ID
		}
	}
	return ""
}

func getZone(zones *[]string, endpoint *endpoint.Endpoint) (string, error) {
	var matchZoneName = ""
	err := fmt.Errorf("unable find matching zone for the endpoint %s", endpoint)
	for _, zone := range *zones {
		if (endpoint.DNSName == zone || strings.HasSuffix(endpoint.DNSName, "."+zone)) && len(zone) > len(matchZoneName) {
			matchZoneName = zone
			err = nil
		}
	}
	// Fallback for external-dns type-prefixed TXT records on apex domains.
	// e.g., _edns.a-beersandbusiness.com where zone is beersandbusiness.com —
	// the zone appears after a hyphen rather than a dot boundary.
	if matchZoneName == "" {
		for _, zone := range *zones {
			if strings.HasSuffix(endpoint.DNSName, "-"+zone) && len(zone) > len(matchZoneName) {
				matchZoneName = zone
				err = nil
			}
		}
	}
	return matchZoneName, err
}
