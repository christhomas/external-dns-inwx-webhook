package inwx

import (
	"context"
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
	return &INWXProvider{
		client:       &ClientWrapper{client: inwx.NewClient(username, password, &inwx.ClientOptions{Sandbox: sandbox})},
		domainFilter: endpoint.NewDomainFilter(*domainFilter),
		logger:       logger,
	}
}

func (p *INWXProvider) Records(ctx context.Context) ([]*endpoint.Endpoint, error) {
	endpoints := make([]*endpoint.Endpoint, 0)

	if _, err := p.client.login(); err != nil {
		return nil, err
	}
	defer p.client.logout()

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
	defer p.client.logout()

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
			slog.Error("failed to create DNS record for endpoint", "err", err)
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

	for _, ep := range changes.Create {
		zone, err := getZone(zones, ep)
		if err != nil {
			errs = append(errs, err)
			slog.Error("failed to create DNS record for endpoint", "err", err)
		} else {
			for _, target := range ep.Targets {
				var name string
				if ep.DNSName == zone {
					name = ""
				} else {
					name = strings.TrimSuffix(ep.DNSName, fmt.Sprintf(".%s", zone))
				}
				rec := &inwx.NameserverRecordRequest{
					Domain:  zone,
					Name:    name,
					Type:    ep.RecordType,
					TTL:     int(ep.RecordTTL),
					Content: target,
				}
				if err = p.client.createRecord(rec); err != nil {
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
			if err != nil {
				errs = append(errs, err)
				slog.Error("failed to look up up records to delete", "err", err)
			}
			var name string
			if newEp.DNSName == zone {
				name = ""
			} else {
				name = strings.TrimSuffix(newEp.DNSName, fmt.Sprintf(".%s", zone))
			}
			for j := range max(len(oldEp.Targets), len(newEp.Targets), len(recIDs)) {
				switch {
				case j >= len(newEp.Targets):
					p.client.deleteRecord(recIDs[j])
				case j >= len(oldEp.Targets):
					rec := &inwx.NameserverRecordRequest{
						Domain:  zone,
						Name:    name,
						Type:    newEp.RecordType,
						TTL:     int(newEp.RecordTTL),
						Content: newEp.Targets[j],
					}
					if err = p.client.createRecord(rec); err != nil {
						errs = append(errs, err)
						slog.Error("failed to create record", "rec", rec, "err", err)
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

func getRecIDs(zone string, records *[]inwx.NameserverRecord, ep endpoint.Endpoint) ([]int, error) {
	recIDs := []int{}
	for _, target := range ep.Targets {
		for _, record := range *records {
			var targetDNSName string
			if record.Name == "" {
				targetDNSName = zone
			} else {
				targetDNSName = fmt.Sprintf("%s.%s", record.Name, zone)
			}
			if ep.RecordType == record.Type && target == record.Content && targetDNSName == ep.DNSName {
				recIDs = append(recIDs, record.ID)
			}
		}
	}
	if len(recIDs) != len(ep.Targets) {
		return nil, fmt.Errorf("Failed to map all endpoint targets to entries")
	}
	return recIDs, nil
}

func getZone(zones *[]string, endpoint *endpoint.Endpoint) (string, error) {
	var matchZoneName = ""
	err := fmt.Errorf("unable find matching zone for the endpoint %s", endpoint)
	for _, zone := range *zones {
		if strings.HasSuffix(endpoint.DNSName, zone) && len(zone) > len(matchZoneName) {
			matchZoneName = zone
			err = nil
		}
	}
	return matchZoneName, err
}
