package inwx

import (
	"fmt"

	inwx "github.com/nrdcg/goinwx"
)

type ClientWrapper struct {
	client *inwx.Client
}

type AbstractClientWrapper interface {
	login() (*inwx.LoginResponse, error)
	logout() error
	getRecords(domain string) (*[]inwx.NameserverRecord, error)
	getZones() (*[]string, error)
	createRecord(request *inwx.NameserverRecordRequest) error
	updateRecord(recID int, request *inwx.NameserverRecordRequest) error
	deleteRecord(recID int) error
}

func (w *ClientWrapper) login() (*inwx.LoginResponse, error) {
	return w.client.Account.Login()
}

func (w *ClientWrapper) logout() error {
	return w.client.Account.Logout()
}

func (w *ClientWrapper) getRecords(domain string) (*[]inwx.NameserverRecord, error) {
	records := []inwx.NameserverRecord{}
	zone, err := w.client.Nameservers.Info(&inwx.NameserverInfoRequest{Domain: domain})
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve records for zone %s: %w", domain, err)
	}
	for _, rec := range zone.Records {
		records = append(records, rec)
	}
	return &records, nil
}

func (w *ClientWrapper) getZones() (*[]string, error) {
	zones := []string{}
	response, err := w.client.Nameservers.ListWithParams(&inwx.NameserverListRequest{})
	if err != nil {
		return nil, fmt.Errorf("no domain filter supplied, failed to list nameserver zones: %w", err)
	}
	for _, domain := range response.Domains {
		zones = append(zones, domain.Domain)
	}
	return &zones, nil
}

func (w *ClientWrapper) createRecord(request *inwx.NameserverRecordRequest) error {
	_, err := w.client.Nameservers.CreateRecord(request)
	return err
}

func (w *ClientWrapper) updateRecord(recID int, request *inwx.NameserverRecordRequest) error {
	return w.client.Nameservers.UpdateRecord(recID, request)
}

func (w *ClientWrapper) deleteRecord(recID int) error {
	return w.client.Nameservers.DeleteRecord(recID)
}
