package inwx

import (
	"fmt"
	"maps"
	"slices"
	"strconv"

	inwx "github.com/nrdcg/goinwx"
)

type MockClientWrapper struct {
	db       map[string]*[]inwx.NameserverRecord
	idToZone map[string]string
}

func (w *MockClientWrapper) login() (*inwx.LoginResponse, error) {
	return &inwx.LoginResponse{
		CustomerID: 1000,
		AccountID:  1000,
		TFA:        "",
		BuildDate:  "2000-01-01 00:00:00",
		Version:    "0.0.0",
	}, nil
}

func (w *MockClientWrapper) logout() error {
	return nil
}

func (w *MockClientWrapper) getRecords(domain string) (*[]inwx.NameserverRecord, error) {
	if recs, ok := w.db[domain]; !ok {
		return nil, fmt.Errorf("unable to retrieve records for zone %s: key not found in mock db", domain)
	} else {
		undeletedRecs := []inwx.NameserverRecord{}
		for _, rec := range *recs {
			if rec.ID != "" {
				undeletedRecs = append(undeletedRecs, rec)
			}
		}
		return &undeletedRecs, nil
	}
}

func (w *MockClientWrapper) getZones() (*[]string, error) {
	zones := slices.Collect(maps.Keys(w.db))
	return &zones, nil
}

func (w *MockClientWrapper) createRecord(r *inwx.NameserverRecordRequest) error {
	if recs, ok := w.db[r.Domain]; !ok {
		return fmt.Errorf("zone %s not found", r.Domain)
	} else {
		id := strconv.Itoa(len(*recs))
		newRecs := append(*recs, inwx.NameserverRecord{
			ID:       id,
			Name:     r.Name,
			Type:     r.Type,
			Content:  r.Content,
			TTL:      r.TTL,
			Priority: r.Priority,
		})
		w.idToZone[id] = r.Domain
		w.db[r.Domain] = &newRecs
		return nil
	}
}

func (w *MockClientWrapper) findRecord(recs *[]inwx.NameserverRecord, recID string) int {
	for i, rec := range *recs {
		if rec.ID == recID {
			return i
		}
	}
	return -1
}

func (w *MockClientWrapper) updateRecord(recID string, r *inwx.NameserverRecordRequest) error {
	if recs, ok := w.db[r.Domain]; !ok {
		return fmt.Errorf("zone %s not found", r.Domain)
	} else {
		idx := w.findRecord(recs, recID)
		if idx == -1 {
			return fmt.Errorf("record ID %s not found", recID)
		}
		(*recs)[idx] = inwx.NameserverRecord{
			ID:       recID,
			Name:     r.Name,
			Type:     r.Type,
			Content:  r.Content,
			TTL:      r.TTL,
			Priority: r.Priority,
		}
		return nil
	}
}

func (w *MockClientWrapper) deleteRecord(recID string) error {
	if zone, ok := w.idToZone[recID]; !ok {
		return fmt.Errorf("zone for record ID %s not found", recID)
	} else {
		if recs, ok := w.db[zone]; !ok {
			return fmt.Errorf("zone %s not found", zone)
		} else {
			idx := w.findRecord(recs, recID)
			if idx == -1 {
				return fmt.Errorf("record ID %s not found", recID)
			}
			if (*recs)[idx].ID == "" {
				return fmt.Errorf("record ID %s has already been deleted", recID)
			}
			(*recs)[idx].ID = ""
			return nil
		}
	}
}

func (w *MockClientWrapper) CreateZone(zone string) {
	if _, ok := w.db[zone]; ok {
		panic(fmt.Errorf("zone %s already exists", zone))
	} else {
		w.db[zone] = &[]inwx.NameserverRecord{}
	}
}
