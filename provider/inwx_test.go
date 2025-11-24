package inwx

import (
	"context"
	"log/slog"
	"testing"

	inwx "github.com/nrdcg/goinwx"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
)

func NewINWXProviderWithMockClient(domainFilter *[]string, logger *slog.Logger) (*MockClientWrapper, *INWXProvider) {
	wrapper := &MockClientWrapper{
		db:       make(map[string](*[]inwx.NameserverRecord)),
		idToZone: make(map[int]string),
	}
	return wrapper, &INWXProvider{
		client:       wrapper,
		domainFilter: endpoint.NewDomainFilter(*domainFilter),
		logger:       logger,
	}
}

func TestINWXProvider(t *testing.T) {
	t.Run("EndpointZoneName", testEndpointZoneName)
	t.Run("GetRecIDs", testGetRecIDs)
	t.Run("ApplyChanges", testApplyChanges)
	t.Run("Records", testRecords)
}

func testEndpointZoneName(t *testing.T) {
	w, p := NewINWXProviderWithMockClient(&[]string{"bar.org", "baz.org"}, slog.Default())
	w.CreateZone("bar.org")
	w.CreateZone("baz.org")
	w.CreateZone("subdomain.bar.org")
	zones, _ := p.client.getZones()

	ep1 := endpoint.Endpoint{
		DNSName:    "foo.bar.org",
		Targets:    endpoint.Targets{"5.5.5.5"},
		RecordType: endpoint.RecordTypeA,
	}

	ep2 := endpoint.Endpoint{
		DNSName:    "foo.foo.org",
		Targets:    endpoint.Targets{"5.5.5.5"},
		RecordType: endpoint.RecordTypeA,
	}

	ep3 := endpoint.Endpoint{
		DNSName:    "baz.org",
		Targets:    endpoint.Targets{"5.5.5.5"},
		RecordType: endpoint.RecordTypeA,
	}

	ep4 := endpoint.Endpoint{
		DNSName:    "foo.subdomain.bar.org",
		Targets:    endpoint.Targets{"1.1.1.1"},
		RecordType: endpoint.RecordTypeA,
	}

	ep5 := endpoint.Endpoint{
		DNSName:    "foo.otherdomain.bar.org",
		Targets:    endpoint.Targets{"1.1.1.1"},
		RecordType: endpoint.RecordTypeA,
	}

	z, _ := getZone(zones, &ep1)
	assert.Equal(t, "bar.org", z)
	z, _ = getZone(zones, &ep2)
	assert.Equal(t, "", z)
	z, _ = getZone(zones, &ep3)
	assert.Equal(t, "baz.org", z)
	z, _ = getZone(zones, &ep4)
	assert.Equal(t, "subdomain.bar.org", z)
	z, _ = getZone(zones, &ep5)
	assert.Equal(t, "bar.org", z)
}

func testGetRecIDs(t *testing.T) {

	inwx1 := inwx.NameserverRecord{
		Name:    "foo",
		Type:    "TXT",
		Content: "heritage=external-dns,external-dns/owner=default,external-dns/resource=service/default/nginx",
		ID:      10,
	}

	inwx2 := inwx.NameserverRecord{
		Name:    "foo",
		Type:    "A",
		Content: "5.5.5.5",
		ID:      11,
	}

	inwx3 := inwx.NameserverRecord{
		Name:    "",
		Type:    "A",
		Content: "5.5.5.5",
		ID:      12,
	}

	inwx4 := inwx.NameserverRecord{
		Name:    "",
		Type:    "A",
		Content: "5.5.5.6",
		ID:      13,
	}

	records := []inwx.NameserverRecord{inwx1, inwx2, inwx3, inwx4}

	recIDs, err := getRecIDs("example.com", &records, endpoint.Endpoint{
		DNSName:    "foo.example.com",
		Targets:    []string{"heritage=external-dns,external-dns/owner=default,external-dns/resource=service/default/nginx"},
		RecordType: "TXT",
	})
	assert.NoError(t, err)
	assert.Equal(t, []int{10}, recIDs)

	recIDs, err = getRecIDs("baz.org", &records, endpoint.Endpoint{
		DNSName:    "foo.baz.org",
		Targets:    []string{"5.5.5.5"},
		RecordType: "A",
	})
	assert.NoError(t, err)
	assert.Equal(t, []int{11}, recIDs)

	recIDs, err = getRecIDs("baz.org", &records, endpoint.Endpoint{
		DNSName:    "baz.org",
		Targets:    []string{"5.5.5.5", "5.5.5.6"},
		RecordType: "A",
	})
	assert.NoError(t, err)
	assert.Equal(t, []int{12, 13}, recIDs)

}

func testApplyChanges(t *testing.T) {
	w, p := NewINWXProviderWithMockClient(&[]string{"example.com"}, slog.Default())
	w.CreateZone("example.com")
	var err error
	var recs *[]inwx.NameserverRecord
	ep1 := &endpoint.Endpoint{
		DNSName:    "foo.example.com",
		Targets:    []string{"1.1.1.1"},
		RecordType: "A",
		RecordTTL:  60,
	}
	err = p.ApplyChanges(context.TODO(), &plan.Changes{
		Create:    []*endpoint.Endpoint{ep1},
		Delete:    []*endpoint.Endpoint{},
		UpdateOld: []*endpoint.Endpoint{},
		UpdateNew: []*endpoint.Endpoint{},
	})
	assert.NoError(t, err)
	recs, err = w.getRecords("example.com")
	assert.NoError(t, err)
	assert.Equal(t, &[]inwx.NameserverRecord{{
		ID:      0,
		Name:    "foo",
		Type:    "A",
		Content: "1.1.1.1",
		TTL:     60,
	}}, recs)

	ep2 := &endpoint.Endpoint{
		DNSName:    "foo.example.com",
		Targets:    []string{"1.1.1.2"},
		RecordType: "A",
		RecordTTL:  60,
	}
	err = p.ApplyChanges(context.TODO(), &plan.Changes{
		Create:    []*endpoint.Endpoint{},
		Delete:    []*endpoint.Endpoint{},
		UpdateOld: []*endpoint.Endpoint{ep1},
		UpdateNew: []*endpoint.Endpoint{ep2},
	})
	assert.NoError(t, err)
	recs, err = w.getRecords("example.com")
	assert.NoError(t, err)
	assert.Equal(t, &[]inwx.NameserverRecord{{
		ID:      0,
		Name:    "foo",
		Type:    "A",
		Content: "1.1.1.2",
		TTL:     60,
	}}, recs)

	err = p.ApplyChanges(context.TODO(), &plan.Changes{
		Create:    []*endpoint.Endpoint{},
		Delete:    []*endpoint.Endpoint{ep2},
		UpdateOld: []*endpoint.Endpoint{},
		UpdateNew: []*endpoint.Endpoint{},
	})
	assert.NoError(t, err)
	recs, err = w.getRecords("example.com")
	assert.NoError(t, err)
	assert.Equal(t, &[]inwx.NameserverRecord{}, recs)
}

func testRecords(t *testing.T) {
	_, p := NewINWXProviderWithMockClient(&[]string{"example.com"}, slog.Default())
	ep, err := p.Records(context.TODO())
	assert.Equal(t, []*endpoint.Endpoint{}, ep)
	assert.NoError(t, err)
}
